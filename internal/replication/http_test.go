package replication

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"
)

func TestSignRequestBindsTimestampAndBody(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	body := []byte(`{"event":"test"}`)
	sig := SignRequest(key, "1792368000", body)
	provided, err := hex.DecodeString(sig[len("sha256="):])
	if err != nil { t.Fatal(err) }
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("1792368000."))
	_, _ = mac.Write(body)
	if !hmac.Equal(provided, mac.Sum(nil)) { t.Fatal("signature does not match HMAC-SHA256") }
	if SignRequest(key, "1792368001", body) == sig { t.Fatal("signature must bind timestamp") }
	if SignRequest(key, "1792368000", []byte(`{"event":"changed"}`)) == sig { t.Fatal("signature must bind body") }
}

func TestTimestampSkewHelper(t *testing.T) {
	now := time.Unix(1792368000, 0)
	if absDuration(now.Sub(time.Unix(1792368000, 0))) > 5*time.Minute { t.Fatal("current timestamp rejected") }
	if absDuration(now.Sub(time.Unix(1792367000, 0))) <= 5*time.Minute { t.Fatal("old timestamp should be rejected") }
}
