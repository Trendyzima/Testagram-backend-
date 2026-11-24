package main

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

func TestMailPlainTextAlternativeStripsMarkupAndDecodesEntities(t *testing.T) {
	got := mailPlainTextFromHTML("<p>Hello &amp; welcome</p><p>Open <strong>Testagram</strong></p>")
	if got != "Hello & welcome\nOpen Testagram" {
		t.Fatalf("plain-text alternative = %q", got)
	}
}

func TestMailPlainTextPreservesUnsubscribeLinkDestination(t *testing.T) {
	got := mailPlainTextFromHTML(`<p>Manage your settings: <a href="https://testagram.site/api/email/unsubscribe?token=abc">unsubscribe</a></p>`)
	if !strings.Contains(got, "unsubscribe (https://testagram.site/api/email/unsubscribe?token=abc)") {
		t.Fatalf("plain-text alternative lost unsubscribe destination: %q", got)
	}
}

func TestBrandedMailShellEscapesSubjectAndIncludesAccessibleLayout(t *testing.T) {
	t.Setenv("AUTH_PUBLIC_BASE_URL", "https://testagram.site")
	got := brandedTestagramMailHTML("<welcome>", "<p>Hello</p>")
	if !strings.Contains(got, "&lt;welcome&gt;") || !strings.Contains(got, "role=\"presentation\"") ||
		!strings.Contains(got, "lang=\"en\"") || !strings.Contains(got, "<p>Hello</p>") {
		t.Fatalf("branded email shell is missing expected accessible structure: %s", got)
	}
}

func TestMailCipherEncryptsAndDecrypts(t *testing.T) {
	key := strings.Repeat("ab", 32)
	t.Setenv("TESTAGRAM_MAIL_ENCRYPTION_KEY", key)

	aead, err := mailCipher()
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte(`{"recipient":"person@example.test","subject":"test","html":"secret-link"}`)
	nonce := make([]byte, aead.NonceSize())
	for i := range nonce {
		nonce[i] = byte(i + 1)
	}
	ciphertext := aead.Seal(nil, nonce, plain, []byte("testagram-mail-v1"))
	if string(ciphertext) == string(plain) {
		t.Fatal("mail payload was not encrypted")
	}
	got, err := aead.Open(nil, nonce, ciphertext, []byte("testagram-mail-v1"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(plain) {
		t.Fatalf("decrypted payload mismatch: %q", got)
	}
	if _, err := hex.DecodeString(key); err != nil {
		t.Fatal(err)
	}
}

func TestMailCipherRejectsMissingOrWeakKey(t *testing.T) {
	keys := []string{"", "abcd", strings.Repeat("z", 64), strings.Repeat("ab", 31)}
	for i, key := range keys {
		t.Run(fmt.Sprintf("case-%d", i), func(t *testing.T) {
			t.Setenv("TESTAGRAM_MAIL_ENCRYPTION_KEY", key)
			if _, err := mailCipher(); err == nil {
				t.Fatal("invalid mail encryption key accepted")
			}
		})
	}
}
