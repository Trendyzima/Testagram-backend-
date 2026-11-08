package realtime

import (
	"encoding/base64"
	"testing"
)

func TestSignPublish(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	payload := []byte(`{"event_id":"m-1","event_type":"message.created"}`)
	signature, err := SignPublish(secret, payload)
	if err != nil { t.Fatal(err) }
	if _, err := base64.RawURLEncoding.DecodeString(signature); err != nil { t.Fatalf("signature is not base64url: %v", err) }
	if _, err := SignPublish("weak", payload); err == nil { t.Fatal("expected weak secret to be rejected") }
	if _, err := SignPublish(secret, nil); err == nil { t.Fatal("expected empty payload to be rejected") }
}
