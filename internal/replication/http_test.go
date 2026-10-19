package replication

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSignRequestBindsTimestampAndBody(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	body := []byte(`{"event":"test"}`)
	sig := SignRequest(key, "1792368000", body)
	provided, err := hex.DecodeString(sig[len("sha256="):])
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("1792368000."))
	_, _ = mac.Write(body)
	if !hmac.Equal(provided, mac.Sum(nil)) {
		t.Fatal("signature does not match HMAC-SHA256")
	}
	if SignRequest(key, "1792368001", body) == sig {
		t.Fatal("signature must bind timestamp")
	}
	if SignRequest(key, "1792368000", []byte(`{"event":"changed"}`)) == sig {
		t.Fatal("signature must bind body")
	}
}

func TestTimestampSkewHelper(t *testing.T) {
	now := time.Unix(1792368000, 0)
	if absDuration(now.Sub(time.Unix(1792368000, 0))) > 5*time.Minute {
		t.Fatal("current timestamp rejected")
	}
	if absDuration(now.Sub(time.Unix(1792367000, 0))) <= 5*time.Minute {
		t.Fatal("old timestamp should be rejected")
	}
}

func TestQueueReceiverRejectsUnknownOriginBeforeDatabaseAccess(t *testing.T) {
	key := bytes.Repeat([]byte{0x31}, 32)
	receiver, err := NewQueueReceiver(&Store{}, map[string][]byte{"xclone": key})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{}`)
	req := httptest.NewRequest(http.MethodPost, "/internal/replication/events", bytes.NewReader(body))
	req.Header.Set(HeaderOrigin, "unknown-peer")
	req.Header.Set(HeaderTimestamp, "1792368000")
	req.Header.Set(HeaderSignature, SignRequest(key, "1792368000", body))
	recorder := httptest.NewRecorder()
	receiver.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("got status %d, want 401", recorder.Code)
	}
}

func TestQueueReceiverRejectsExpiredTimestampBeforeDatabaseAccess(t *testing.T) {
	key := bytes.Repeat([]byte{0x31}, 32)
	receiver, err := NewQueueReceiver(&Store{}, map[string][]byte{"xclone": key})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{}`)
	req := httptest.NewRequest(http.MethodPost, "/internal/replication/events", bytes.NewReader(body))
	req.Header.Set(HeaderOrigin, "xclone")
	req.Header.Set(HeaderTimestamp, "1000000000")
	req.Header.Set(HeaderSignature, SignRequest(key, "1000000000", body))
	recorder := httptest.NewRecorder()
	receiver.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("got status %d, want 401", recorder.Code)
	}
}

func TestQueueReceiverRejectsBadSignatureBeforeDatabaseAccess(t *testing.T) {
	key := bytes.Repeat([]byte{0x31}, 32)
	receiver, err := NewQueueReceiver(&Store{}, map[string][]byte{"xclone": key})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{}`)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	req := httptest.NewRequest(http.MethodPost, "/internal/replication/events", bytes.NewReader(body))
	req.Header.Set(HeaderOrigin, "xclone")
	req.Header.Set(HeaderTimestamp, timestamp)
	req.Header.Set(HeaderSignature, "sha256="+strings.Repeat("0", 64))
	recorder := httptest.NewRecorder()
	receiver.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("got status %d, want 401", recorder.Code)
	}
}

func TestNewQueueReceiverRejectsShortPeerKeys(t *testing.T) {
	_, err := NewQueueReceiver(&Store{}, map[string][]byte{"xclone": []byte("too-short")})
	if err == nil {
		t.Fatal("expected short peer key to be rejected")
	}
}

func TestReceiverRejectsNonPostMethod(t *testing.T) {
	key := bytes.Repeat([]byte{0x41}, 32)
	receiver, err := NewQueueReceiver(&Store{}, map[string][]byte{"xclone": key})
	if err != nil { t.Fatal(err) }
	req := httptest.NewRequest(http.MethodGet, "/internal/replication/events", nil)
	recorder := httptest.NewRecorder()
	receiver.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("got status %d, want 405", recorder.Code)
	}
	if recorder.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("Allow header = %q, want POST", recorder.Header().Get("Allow"))
	}
}

func TestNewReceiverRequiresDomainProjector(t *testing.T) {
	key := bytes.Repeat([]byte{0x41}, 32)
	if _, err := NewReceiver(&Store{}, map[string][]byte{"xclone": key}, nil); err == nil {
		t.Fatal("expected synchronous receiver to require a projector")
	}
}

func TestQueueReceiverRejectsOversizedBodyBeforeDatabaseAccess(t *testing.T) {
	key := bytes.Repeat([]byte{0x31}, 32)
	receiver, err := NewQueueReceiver(&Store{}, map[string][]byte{"xclone": key})
	if err != nil { t.Fatal(err) }
	body := bytes.Repeat([]byte("a"), defaultBodyLimit+1)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	req := httptest.NewRequest(http.MethodPost, "/internal/replication/events", bytes.NewReader(body))
	req.Header.Set(HeaderOrigin, "xclone")
	req.Header.Set(HeaderTimestamp, timestamp)
	req.Header.Set(HeaderSignature, SignRequest(key, timestamp, body))
	recorder := httptest.NewRecorder()
	receiver.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("got status %d, want 413", recorder.Code)
	}
}

func TestQueueReceiverRejectsUnknownEnvelopeFieldsBeforeDatabaseAccess(t *testing.T) {
	key := bytes.Repeat([]byte{0x31}, 32)
	receiver, err := NewQueueReceiver(&Store{}, map[string][]byte{"xclone": key})
	if err != nil { t.Fatal(err) }
	body := []byte(`{"id":"018f1f3a-7b2c-7abc-8def-0123456789ab","origin":"xclone","entity_type":"post","entity_id":"post:1","entity_version":1,"schema_version":1,"event_type":"post.created","occurred_at":"2026-10-19T00:00:00Z","payload":{"visibility":"public"},"unexpected":true}`)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	req := httptest.NewRequest(http.MethodPost, "/internal/replication/events", bytes.NewReader(body))
	req.Header.Set(HeaderOrigin, "xclone")
	req.Header.Set(HeaderTimestamp, timestamp)
	req.Header.Set(HeaderSignature, SignRequest(key, timestamp, body))
	recorder := httptest.NewRecorder()
	receiver.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400", recorder.Code)
	}
}
