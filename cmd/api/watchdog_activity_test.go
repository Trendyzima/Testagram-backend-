package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func setWatchdogTestEnv(t *testing.T, name, value string) {
	t.Helper()
	previous, existed := os.LookupEnv(name)
	if err := os.Setenv(name, value); err != nil { t.Fatal(err) }
	t.Cleanup(func() {
		if existed { _ = os.Setenv(name, previous) } else { _ = os.Unsetenv(name) }
	})
}

func TestWatchdogBucketKeyIsStableAndContainsNoIdentity(t *testing.T) {
	at := time.Date(2026, 11, 15, 17, 42, 0, 0, time.FixedZone("EAT", 3*60*60))
	a := watchdogBucketKey("feed_open", at)
	b := watchdogBucketKey("feed_open", at.Add(4*time.Minute))
	c := watchdogBucketKey("feed_open", at.Add(5*time.Minute))
	if a != b { t.Fatalf("same bucket should deduplicate: %q != %q", a, b) }
	if a == c { t.Fatalf("next bucket should allow a fresh wake-up: %q", a) }
	if strings.Contains(a, "@") || strings.Contains(a, "user") || strings.Contains(a, "email") {
		t.Fatalf("key must not contain identity data: %q", a)
	}
	if len(a) < 16 || len(a) > 128 { t.Fatalf("invalid key length: %d", len(a)) }
}

func TestEnqueueWatchdogImpulseUsesPrivateCoarseEventContract(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != http.MethodPost || r.URL.Path != "/v1/watchdog/impulses" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer watchdog-test-secret" {
			t.Errorf("dedicated service bearer missing")
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil { t.Errorf("decode body: %v", err) }
		if body["eventType"] != "login" || !strings.HasPrefix(body["idempotencyKey"], "testagram:login:") || len(body) != 2 {
			t.Errorf("unexpected coarse impulse payload: %#v", body)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	setWatchdogTestEnv(t, "VPS_WATCHDOG_INTERNAL_URL", server.URL)
	setWatchdogTestEnv(t, "VPS_WATCHDOG_INTERNAL_TOKEN", "watchdog-test-secret")
	(&service{}).enqueueWatchdogImpulse("login")
	if !called { t.Fatal("watchdog impulse was not sent") }
}

func TestEnqueueWatchdogImpulseIgnoresUnknownEvent(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	setWatchdogTestEnv(t, "VPS_WATCHDOG_INTERNAL_URL", server.URL)
	setWatchdogTestEnv(t, "VPS_WATCHDOG_INTERNAL_TOKEN", "watchdog-test-secret")
	(&service{}).enqueueWatchdogImpulse("run-arbitrary-command")
	if called { t.Fatal("unsupported event reached watchdog control plane") }
}
