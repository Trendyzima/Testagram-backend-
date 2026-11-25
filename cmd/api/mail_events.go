package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/mail"
	"os"
	"strings"
)

// mailRecipientFingerprint is used for suppression and unsubscribe safety.
// It is keyed by Testagram's persistent, VPS-generated encryption key.
func mailRecipientFingerprint(recipient string) ([]byte, error) {
	key, err := hex.DecodeString(strings.TrimSpace(os.Getenv("TESTAGRAM_MAIL_ENCRYPTION_KEY")))
	if err != nil || len(key) != 32 {
		return nil, errors.New("mail encryption key unavailable")
	}
	parsed, err := mail.ParseAddress(recipient)
	if err != nil {
		return nil, errors.New("invalid recipient address")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(strings.ToLower(strings.TrimSpace(parsed.Address))))
	return mac.Sum(nil), nil
}
