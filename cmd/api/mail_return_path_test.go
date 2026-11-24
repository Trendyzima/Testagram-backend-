package main

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestNativeMailReturnPathIsStableAndOpaque(t *testing.T) {
	t.Setenv("TESTAGRAM_MAIL_VERP_KEY", strings.Repeat("ab", 32))
	address, firstHash, err := nativeMailReturnPath(42)
	if err != nil {
		t.Fatal(err)
	}
	secondAddress, secondHash, err := nativeMailReturnPath(42)
	if err != nil {
		t.Fatal(err)
	}
	otherAddress, _, err := nativeMailReturnPath(43)
	if err != nil {
		t.Fatal(err)
	}
	if address != secondAddress || string(firstHash) != string(secondHash) {
		t.Fatal("same outbox ID did not produce a stable VERP return path")
	}
	if address == otherAddress {
		t.Fatal("different outbox IDs produced the same VERP return path")
	}
	if !strings.HasPrefix(address, "b+") || !strings.HasSuffix(address, "@bounces.testagram.site") {
		t.Fatalf("unexpected VERP address: %q", address)
	}
	token := strings.TrimSuffix(strings.TrimPrefix(address, "b+"), "@bounces.testagram.site")
	if len(token) != 32 {
		t.Fatalf("VERP token length=%d, want 32", len(token))
	}
	hash := sha256.Sum256([]byte(token))
	if string(firstHash) != string(hash[:]) {
		t.Fatal("stored VERP hash does not match the opaque return-path token")
	}
}

func TestNativeMailReturnPathRequiresDedicatedVERPKey(t *testing.T) {
	t.Setenv("TESTAGRAM_MAIL_ENCRYPTION_KEY", strings.Repeat("cd", 32))
	t.Setenv("TESTAGRAM_MAIL_VERP_KEY", "")
	if _, _, err := nativeMailReturnPath(1); err == nil {
		t.Fatal("VERP generation fell back to the payload-encryption key")
	}
}

func TestNativeMailReturnPathRejectsInvalidIDsAndKeys(t *testing.T) {
	t.Setenv("TESTAGRAM_MAIL_VERP_KEY", strings.Repeat("ab", 32))
	for _, id := range []int64{0, -1} {
		if _, _, err := nativeMailReturnPath(id); err == nil {
			t.Errorf("invalid outbox ID %d was accepted", id)
		}
	}
	t.Setenv("TESTAGRAM_MAIL_VERP_KEY", "not-a-hex-key")
	if _, _, err := nativeMailReturnPath(1); err == nil {
		t.Fatal("invalid VERP key was accepted")
	}
	if _, err := hex.DecodeString("ab"); err != nil {
		t.Fatal(err)
	}
}
