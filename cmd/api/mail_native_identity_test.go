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
	if err != nil { t.Fatal(err) }
	keyPath := filepath.Join(t.TempDir(), "dkim.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(keyPath, pemBytes, 0600); err != nil { t.Fatal(err) }
	t.Setenv("TESTAGRAM_DKIM_PRIVATE_KEY_FILE", keyPath)
	t.Setenv("TESTAGRAM_DKIM_SELECTOR", "testselector")

	message := []byte("From: Testagram <no-reply@testagram.site>\r\nTo: user@example.net\r\nSubject: hello\r\nDate: Tue, 24 Nov 2026 12:00:00 +0000\r\nMessage-ID: <abc@testagram.site>\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\nhello   world\r\n")
	signed, err := signNativeMailDKIM(message)
	if err != nil { t.Fatal(err) }
	if !bytes.HasPrefix(signed, []byte("DKIM-Signature: ")) { t.Fatal("DKIM signature header was not prepended") }
	headerEnd := bytes.Index(signed, []byte("\r\n\r\n"))
	if headerEnd < 0 { t.Fatal("signed message separator missing") }
	header := string(signed[:headerEnd])
	line := strings.SplitN(header, "\r\n", 2)[0]
	value := strings.TrimPrefix(line, "DKIM-Signature: ")
	parts := strings.Split(value, ";")
	var signatureText string
	for _, part := range parts {
		if strings.HasPrefix(strings.TrimSpace(part), "b=") { signatureText = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(part), "b=")) }
	}
	if signatureText == "" { t.Fatal("DKIM signature value missing") }
	signature, err := base64.StdEncoding.DecodeString(signatureText)
	if err != nil { t.Fatal(err) }
	// Rebuild the signed header input with the b= value empty, as required by RFC 6376.
	unsigned := strings.Replace(line, "b="+signatureText, "b=", 1)
	canonicalHeaders, err := dkimSelectHeaders(message[:bytes.Index(message, []byte("\r\n\r\n"))], []string{"from","to","subject","date","message-id","mime-version","content-type"})
	if err != nil { t.Fatal(err) }
	signingInput := append(canonicalHeaders, []byte(dkimRelaxedHeader("DKIM-Signature", strings.TrimPrefix(unsigned, "DKIM-Signature: ")) )...)
	digest := sha256.Sum256(signingInput)
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], signature); err != nil { t.Fatalf("signature did not verify: %v", err) }
}

func TestNativeMailReturnPathIsStableAndOpaque(t *testing.T) {
	t.Setenv("TESTAGRAM_MAIL_ENCRYPTION_KEY", strings.Repeat("ab", 32))
	first, firstHash, err := nativeMailReturnPath(12345)
	if err != nil { t.Fatal(err) }
	second, secondHash, err := nativeMailReturnPath(12345)
	if err != nil { t.Fatal(err) }
	if first != second || !bytes.Equal(firstHash, secondHash) { t.Fatal("return path must be stable for an outbox row") }
	if !strings.HasSuffix(first, "@bounces.testagram.site") || strings.Contains(first, "recipient") { t.Fatalf("unexpected return path: %q", first) }
	if len(firstHash) != 32 { t.Fatalf("unexpected token hash length: %d", len(firstHash)) }
}
