package main

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDKIMRelaxedBodyCanonicalization(t *testing.T) {
	got := string(dkimRelaxedBody([]byte("alpha \t  beta  \r\n\r\n")))
	if got != "alpha beta\r\n" {
		t.Fatalf("unexpected relaxed body: %q", got)
	}
}

func TestSignNativeMailDKIMProducesVerifiableSignature(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "dkim.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	if err := os.WriteFile(keyPath, pemBytes, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TESTAGRAM_DKIM_PRIVATE_KEY_FILE", keyPath)
	t.Setenv("TESTAGRAM_DKIM_SELECTOR", "testselector")

	message := []byte("From: Testagram <no-reply@testagram.site>\r\n" +
		"To: user@example.net\r\nSubject: hello\r\n" +
		"Date: Tue, 24 Nov 2026 12:00:00 +0000\r\n" +
		"Message-ID: <abc@testagram.site>\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n\r\nhello   world\r\n")
	signed, err := signNativeMailDKIM(message)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(signed, []byte("DKIM-Signature: ")) {
		t.Fatal("DKIM signature header was not prepended")
	}
	headerEnd := bytes.Index(signed, []byte("\r\n\r\n"))
	if headerEnd < 0 {
		t.Fatal("signed message separator missing")
	}
	header := string(signed[:headerEnd])
	line := strings.SplitN(header, "\r\n", 2)[0]
	value := strings.TrimPrefix(line, "DKIM-Signature: ")
	parts := strings.Split(value, ";")
	var signatureText string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "b=") {
			signatureText = strings.TrimSpace(strings.TrimPrefix(part, "b="))
		}
	}
	if signatureText == "" {
		t.Fatal("DKIM signature value missing")
	}
	signature, err := base64.StdEncoding.DecodeString(signatureText)
	if err != nil {
		t.Fatal(err)
	}
	unsigned := strings.Replace(line, "b="+signatureText, "b=", 1)
	originalHeaderEnd := bytes.Index(message, []byte("\r\n\r\n"))
	canonicalHeaders, err := dkimSelectHeaders(
		message[:originalHeaderEnd],
		[]string{"from", "to", "subject", "date", "message-id", "mime-version", "content-type"},
	)
	if err != nil {
		t.Fatal(err)
	}
	signingInput := append(
		canonicalHeaders,
		[]byte(dkimRelaxedHeader("DKIM-Signature", strings.TrimPrefix(unsigned, "DKIM-Signature: ")))...,
	)
	digest := sha256.Sum256(signingInput)
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], signature); err != nil {
		t.Fatalf("signature did not verify: %v", err)
	}
}

func TestNativeMailReturnPathIsStableAndOpaque(t *testing.T) {
	t.Setenv("TESTAGRAM_MAIL_ENCRYPTION_KEY", strings.Repeat("ab", 32))
	first, firstHash, err := nativeMailReturnPath(12345)
	if err != nil {
		t.Fatal(err)
	}
	second, secondHash, err := nativeMailReturnPath(12345)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || !bytes.Equal(firstHash, secondHash) {
		t.Fatal("return path must be stable for an outbox row")
	}
	if !strings.HasSuffix(first, "@bounces.testagram.site") || strings.Contains(first, "recipient") {
		t.Fatalf("unexpected return path: %q", first)
	}
	if len(firstHash) != 32 {
		t.Fatalf("unexpected token hash length: %d", len(firstHash))
	}
}

func TestParseNativeDSNHardBounce(t *testing.T) {
	raw := []byte("From: Mail Delivery Subsystem <mailer-daemon@example.net>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/report; report-type=delivery-status; boundary=\"dsn-boundary\"\r\n\r\n" +
		"--dsn-boundary\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nDelivery failed\r\n" +
		"--dsn-boundary\r\nContent-Type: message/delivery-status\r\n\r\n" +
		"Reporting-MTA: dns; mx.example.net\r\n\r\n" +
		"Final-Recipient: rfc822; user@example.net\r\n" +
		"Action: failed\r\nStatus: 5.1.1\r\n" +
		"Diagnostic-Code: smtp; 550 mailbox unavailable\r\n" +
		"--dsn-boundary--\r\n")
	report, err := parseNativeBounceReport(raw)
	if err != nil {
		t.Fatal(err)
	}
	if report.EventType != "dsn.bounced" || report.Recipient != "user@example.net" || report.Status != "5.1.1" {
		t.Fatalf("unexpected parsed DSN: %+v", report)
	}
}

func TestParseNativeDSNRejectsNonReport(t *testing.T) {
	_, err := parseNativeBounceReport([]byte("From: user@example.net\r\nSubject: hello\r\n\r\nNot a DSN"))
	if err == nil {
		t.Fatal("non-report message was accepted as a DSN")
	}
}
