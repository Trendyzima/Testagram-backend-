package main

import (
	"strings"
	"testing"
)

func TestMailRecipientFingerprintIsStableAndCaseInsensitive(t *testing.T) {
	t.Setenv("TESTAGRAM_MAIL_ENCRYPTION_KEY", strings.Repeat("4c", 32))
	first, err := mailRecipientFingerprint("Person@Example.test")
	if err != nil { t.Fatal(err) }
	second, err := mailRecipientFingerprint("person@example.test")
	if err != nil { t.Fatal(err) }
	if string(first) != string(second) || len(first) != 32 {
		t.Fatalf("recipient fingerprint mismatch: %x / %x", first, second)
	}
}
