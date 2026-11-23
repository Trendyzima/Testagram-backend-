package main

import "testing"

func mustMailFingerprint(t *testing.T, recipient string) []byte {
	t.Helper()
	fingerprint, err := mailRecipientFingerprint(recipient)
	if err != nil {
		t.Fatal(err)
	}
	return fingerprint
}
