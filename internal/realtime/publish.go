package realtime

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
)

// SignPublish returns the base64url HMAC-SHA256 signature required by the
// Data Centre realtime publish endpoint. The signed bytes must be the exact
// JSON body sent over HTTP.
func SignPublish(secret string, payload []byte) (string, error) {
	secret = strings.TrimSpace(secret)
	if len([]byte(secret)) < 32 || len(payload) == 0 || len(payload) > 64*1024 {
		return "", errors.New("invalid realtime publish signing input")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}
