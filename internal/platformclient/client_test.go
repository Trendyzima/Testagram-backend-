package platformclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := New(server.URL, "unit-test-secret", time.Second)
	if err != nil { t.Fatal(err) }
	return client
}

func TestNewRequiresValidURLAndToken(t *testing.T) {
	if _, err := New("not-a-url", "token", time.Second); err == nil { t.Fatal("expected URL validation error") }
	if _, err := New("http://localhost", "", time.Second); err == nil { t.Fatal("expected token validation error") }
}

func TestCacheSetSendsBearerAndTTL(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/cache/set" { t.Errorf("unexpected request: %s %s", r.Method, r.URL) }
		if r.Header.Get("Authorization") != "Bearer unit-test-secret" { t.Errorf("missing internal bearer token") }
		if r.Header.Get("Content-Type") != "application/json" { t.Errorf("missing content type") }
		var body struct {
			Key string `json:"key"`
			Value string `json:"value"`
			TTL int `json:"ttl_seconds"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil { t.Fatal(err) }
		if body.Key != "post:1" || body.Value != "cached" || body.TTL != 2 { t.Errorf("unexpected request body: %+v", body) }
		w.WriteHeader(http.StatusNoContent)
	})
	if err := client.Set(context.Background(), "post:1", "cached", 1500*time.Millisecond); err != nil { t.Fatal(err) }
	if err := client.Set(context.Background(), "x", "y", 25*time.Hour); err == nil { t.Fatal("expected TTL bound error") }
}

func TestCacheGetAndMiss(t *testing.T) {
	t.Run("hit", func(t *testing.T) {
		client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("key") != "hello world" { t.Errorf("query value not encoded: %s", r.URL.RawQuery) }
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"value":"there"}`))
		})
		got, err := client.Get(context.Background(), "hello world")
		if err != nil || got != "there" { t.Fatalf("Get() = %q, %v", got, err) }
	})
	t.Run("miss", func(t *testing.T) {
		client := testClient(t, func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "not found", http.StatusNotFound) })
		_, err := client.Get(context.Background(), "missing")
		if !errors.Is(err, ErrNotFound) { t.Fatalf("expected ErrNotFound, got %v", err) }
	})
}

func TestRateLimitAndEnqueue(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer unit-test-secret" { t.Errorf("missing authorization") }
		switch r.URL.Path {
		case "/v1/rate-limit/check":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil { t.Fatal(err) }
			if body["window_seconds"] != float64(60) { t.Errorf("unexpected window: %#v", body) }
			_, _ = w.Write([]byte(`{"allowed":true,"count":1,"remaining":4,"retry_after_seconds":60}`))
		case "/v1/queue/enqueue":
			_, _ = w.Write([]byte(`{"id":"1720000000000-0"}`))
		default:
			http.NotFound(w, r)
		}
	})
	result, err := client.Check(context.Background(), "login:user-1", 5, 60*time.Second)
	if err != nil || !result.Allowed || result.Remaining != 4 { t.Fatalf("Check() = %+v, %v", result, err) }
	id, err := client.Enqueue(context.Background(), "mail", map[string]string{"message_id":"m-1"})
	if err != nil || id != "1720000000000-0" { t.Fatalf("Enqueue() = %q, %v", id, err) }
	if _, err := client.Enqueue(context.Background(), "mail", map[string]string{}); err == nil { t.Fatal("expected empty payload error") }
}

func TestNonSuccessReturnsStatusError(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
	err := client.Delete(context.Background(), "key")
	var status *StatusError
	if !errors.As(err, &status) || status.StatusCode != http.StatusUnauthorized { t.Fatalf("expected status error, got %v", err) }
	if !strings.Contains(err.Error(), "401") { t.Fatalf("unexpected error: %v", err) }
}
