package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

func nativeMailReturnPath(outboxID int64) (string, []byte, error) {
	if outboxID <= 0 { return "", nil, errors.New("invalid mail outbox identifier") }
	key, err := hex.DecodeString(strings.TrimSpace(os.Getenv("TESTAGRAM_MAIL_ENCRYPTION_KEY")))
	if err != nil || len(key) != 32 { return "", nil, errors.New("mail encryption key unavailable") }
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(fmt.Sprintf("testagram-mail-verp-v1:%d", outboxID)))
	token := base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:24])
	tokenHash := sha256.Sum256([]byte(token))
	return "b+" + token + "@bounces.testagram.site", tokenHash[:], nil
}
