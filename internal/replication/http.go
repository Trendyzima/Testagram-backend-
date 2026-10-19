package replication

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	HeaderOrigin    = "X-Replication-Origin"
	HeaderTimestamp = "X-Replication-Timestamp"
	HeaderSignature = "X-Replication-Signature"
	defaultBodyLimit = 1 << 20
)

// ApplyFunc must apply the event's domain projection using tx. It must not
// commit tx. Inbox receipt, version checks, and the projection commit together.
type ApplyFunc func(ctx context.Context, tx pgx.Tx, event Event) error

// Receiver authenticates peer requests and delegates atomic event application.
type Receiver struct {
	store       *Store
	peerKeys    map[string][]byte
	apply       ApplyFunc
	maxSkew     time.Duration
	bodyLimit   int64
	now         func() time.Time
}

func NewReceiver(store *Store, peerKeys map[string][]byte, apply ApplyFunc) (*Receiver, error) {
	if apply == nil { return nil, errors.New("event apply function is required") }
	return newReceiver(store, peerKeys, apply)
}

// NewQueueReceiver authenticates peer events and durably stores them in the
// inbox for a separately configured domain consumer. It does not claim that
// an event has been applied to product data.
func NewQueueReceiver(store *Store, peerKeys map[string][]byte) (*Receiver, error) {
	return newReceiver(store, peerKeys, nil)
}

func newReceiver(store *Store, peerKeys map[string][]byte, apply ApplyFunc) (*Receiver, error) {
	if store == nil { return nil, errors.New("replication store is required") }
	if len(peerKeys) == 0 { return nil, errors.New("at least one peer key is required") }
	keys := make(map[string][]byte, len(peerKeys))
	for origin, key := range peerKeys {
		if !identityPattern.MatchString(origin) { return nil, fmt.Errorf("invalid peer origin %q", origin) }
		if len(key) < 32 { return nil, fmt.Errorf("peer key for %q must be at least 32 bytes", origin) }
		keys[origin] = append([]byte(nil), key...)
	}
	return &Receiver{store:store, peerKeys:keys, apply:apply, maxSkew:5*time.Minute, bodyLimit:defaultBodyLimit, now:time.Now}, nil
}

func (r *Receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	origin := req.Header.Get(HeaderOrigin)
	key, ok := r.peerKeys[origin]
	if !ok {
		http.Error(w, "unauthorized peer", http.StatusUnauthorized)
		return
	}
	tsRaw := req.Header.Get(HeaderTimestamp)
	ts, err := strconv.ParseInt(tsRaw, 10, 64)
	if err != nil || absDuration(r.now().Sub(time.Unix(ts, 0))) > r.maxSkew {
		http.Error(w, "invalid replication timestamp", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, r.bodyLimit))
	if err != nil {
		http.Error(w, "request body too large or unreadable", http.StatusRequestEntityTooLarge)
		return
	}
	provided, err := hex.DecodeString(strings.TrimPrefix(req.Header.Get(HeaderSignature), "sha256="))
	if err != nil || len(provided) != sha256.Size {
		http.Error(w, "invalid replication signature", http.StatusUnauthorized)
		return
	}
	expected := signRequest(key, tsRaw, body)
	if !hmac.Equal(provided, expected) {
		http.Error(w, "invalid replication signature", http.StatusUnauthorized)
		return
	}
	var event Event
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&event); err != nil {
		http.Error(w, "invalid event envelope", http.StatusBadRequest)
		return
	}
	if err := ensureJSONEOF(decoder); err != nil {
		http.Error(w, "invalid event envelope", http.StatusBadRequest)
		return
	}
	if event.Origin != origin {
		http.Error(w, "event origin does not match authenticated peer", http.StatusForbidden)
		return
	}
	if err := event.Validate(); err != nil {
		http.Error(w, "invalid event envelope", http.StatusBadRequest)
		return
	}
	if r.apply == nil {
		inserted, err := r.store.RecordInbox(req.Context(), event)
		if err != nil {
			http.Error(w, "replication event could not be stored", http.StatusServiceUnavailable)
			return
		}
		if inserted {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"status":"queued"}`))
		} else {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"duplicate"}`))
		}
		return
	}
	applied, err := r.store.ApplyInboxTx(req.Context(), event, r.apply)
	if err != nil {
		// Do not return raw database or domain errors to a remote peer.
		http.Error(w, "replication event could not be applied", http.StatusServiceUnavailable)
		return
	}
	if applied {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"applied"}`))
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"duplicate"}`))
}

func ensureJSONEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil { return errors.New("multiple JSON values") }
		return err
	}
	return nil
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 { return -d }
	return d
}

func signRequest(key []byte, timestamp string, body []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(timestamp))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write(body)
	return mac.Sum(nil)
}

// SignRequest signs the exact bytes sent over the wire. The caller should
// marshal the event once and reuse those bytes for both signing and delivery.
func SignRequest(key []byte, timestamp string, body []byte) string {
	return "sha256=" + hex.EncodeToString(signRequest(key, timestamp, body))
}

// RelayOnce claims a bounded batch, delivers each event, and records success or
// a stable retry code. It intentionally never logs event payloads or secrets.
func (s *Store) RelayOnce(ctx context.Context, client *http.Client, endpoint, localOrigin string, peerKey []byte, batchSize int) error {
	if client == nil { return errors.New("HTTP client is required") }
	if !strings.HasPrefix(endpoint, "https://") {
		return errors.New("replication endpoint must use HTTPS")
	}
	if !identityPattern.MatchString(localOrigin) { return errors.New("invalid local origin") }
	if len(peerKey) < 32 { return errors.New("peer key must be at least 32 bytes") }
	events, err := s.ClaimBatch(ctx, batchSize, 30*time.Second)
	if err != nil { return err }
	var firstErr error
	for _, event := range events {
		if event.Origin != localOrigin {
			_ = s.Retry(ctx, event.ID, event.LeaseToken, "origin_mismatch")
			if firstErr == nil { firstErr = errors.New("outbox event origin does not match local origin") }
			continue
		}
		body, marshalErr := json.Marshal(event)
		if marshalErr != nil {
			if firstErr == nil { firstErr = marshalErr }
			_ = s.Retry(ctx, event.ID, event.LeaseToken, "marshal_failed")
			continue
		}
		timestamp := strconv.FormatInt(time.Now().Unix(), 10)
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if reqErr != nil {
			if firstErr == nil { firstErr = reqErr }
			_ = s.Retry(ctx, event.ID, event.LeaseToken, "request_failed")
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(HeaderOrigin, localOrigin)
		req.Header.Set(HeaderTimestamp, timestamp)
		req.Header.Set(HeaderSignature, SignRequest(peerKey, timestamp, body))
		resp, sendErr := client.Do(req)
		if sendErr != nil {
			_ = s.Retry(ctx, event.ID, event.LeaseToken, "peer_unreachable")
			if firstErr == nil { firstErr = sendErr }
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if err := s.MarkDelivered(ctx, event.ID, event.LeaseToken); err != nil && firstErr == nil { firstErr = err }
		} else {
			_ = s.Retry(ctx, event.ID, event.LeaseToken, "peer_http_"+strconv.Itoa(resp.StatusCode))
			if firstErr == nil { firstErr = fmt.Errorf("replication peer returned HTTP %d", resp.StatusCode) }
		}
	}
	return firstErr
}
