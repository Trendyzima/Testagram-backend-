package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestDKIMKey(t *testing.T, mode os.FileMode) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "dkim-private.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), mode); err != nil {
		t.Fatal(err)
	}
	return key, path
}

func TestNativeDKIMSignatureVerifiesCryptographically(t *testing.T) {
	key, path := writeTestDKIMKey(t, 0600)
	t.Setenv("TESTAGRAM_DKIM_PRIVATE_KEY_FILE", path)
	t.Setenv("TESTAGRAM_DKIM_SELECTOR", "test2026")

	message := []byte("From: Testagram <no-reply@testagram.site>\r\n" +
		"To: person@example.test\r\n" +
		"Subject: Native mail test\r\n" +
		"Date: Tue, 24 Nov 2026 10:00:00 +0000\r\n" +
		"Message-ID: <native-test@testagram.site>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\n" +
		"hello   from Testagram\r\n\r\n")
	signed, err := signNativeMailDKIM(message)
	if err != nil {
		t.Fatal(err)
	}
	headerEnd := strings.Index(string(signed), "\r\n\r\n")
	if headerEnd < 0 {
		t.Fatal("signed message has no header/body separator")
	}
	parsed, err := mail.ReadMessage(strings.NewReader(string(signed)))
	if err != nil {
		t.Fatal(err)
	}
	value := parsed.Header.Get("DKIM-Signature")
	if value == "" {
		t.Fatal("DKIM-Signature header is missing")
	}
	var signatureText string
	unsignedParts := make([]string, 0, 8)
	fields := make(map[string]string)
	for _, part := range strings.Split(value, ";") {
		part = strings.TrimSpace(part)
		name, fieldValue, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		fieldValue = strings.TrimSpace(fieldValue)
		fields[name] = fieldValue
		if name == "b" {
			signatureText = fieldValue
			unsignedParts = append(unsignedParts, "b=")
		} else {
			unsignedParts = append(unsignedParts, name+"="+fieldValue)
		}
	}
	signature, err := base64.StdEncoding.DecodeString(signatureText)
	if err != nil {
		t.Fatalf("decode DKIM signature: %v", err)
	}
	if fields["d"] != testagramDKIMDomain || fields["s"] != "test2026" {
		t.Fatalf("unexpected DKIM identity: d=%q s=%q", fields["d"], fields["s"])
	}
	bodyHash := sha256.Sum256(dkimRelaxedBody(signed[headerEnd+4:]))
	if fields["bh"] != base64.StdEncoding.EncodeToString(bodyHash[:]) {
		t.Fatal("DKIM body hash does not match relaxed canonicalized body")
	}
	headerBlock := signed[:headerEnd]
	signedNames := []string{"from", "to", "subject", "date", "message-id", "mime-version", "content-type"}
	canonicalHeaders, err := dkimSelectHeaders(headerBlock, signedNames)
	if err != nil {
		t.Fatal(err)
	}
	unsigned := strings.Join(unsignedParts, "; ")
	signingInput := append(canonicalHeaders, []byte(dkimRelaxedHeader("DKIM-Signature", unsigned))...)
	digest := sha256.Sum256(signingInput)
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], signature); err != nil {
		t.Fatalf("generated DKIM signature did not verify: %v", err)
	}
}

func TestNativeDKIMRejectsWeakKeyPermissions(t *testing.T) {
	_, path := writeTestDKIMKey(t, 0644)
	t.Setenv("TESTAGRAM_DKIM_PRIVATE_KEY_FILE", path)
	t.Setenv("TESTAGRAM_DKIM_SELECTOR", "test2026")
	_, err := signNativeMailDKIM([]byte("From: a@testagram.site\r\nTo: b@example.test\r\nSubject: x\r\nDate: Tue, 24 Nov 2026 10:00:00 +0000\r\nMessage-ID: <x@testagram.site>\r\nMIME-Version: 1.0\r\nContent-Type: text/plain\r\n\r\nx\r\n"))
	if err == nil || !strings.Contains(err.Error(), "permissions") {
		t.Fatalf("expected private-key permission failure, got %v", err)
	}
}

func TestNativeDKIMRejectsInvalidSelector(t *testing.T) {
	_, path := writeTestDKIMKey(t, 0600)
	t.Setenv("TESTAGRAM_DKIM_PRIVATE_KEY_FILE", path)
	t.Setenv("TESTAGRAM_DKIM_SELECTOR", "invalid.selector")
	_, err := signNativeMailDKIM([]byte("From: a@testagram.site\r\nTo: b@example.test\r\nSubject: x\r\nDate: Tue, 24 Nov 2026 10:00:00 +0000\r\nMessage-ID: <x@testagram.site>\r\nMIME-Version: 1.0\r\nContent-Type: text/plain\r\n\r\nx\r\n"))
	if err == nil {
		t.Fatal("invalid DKIM selector was accepted")
	}
}
