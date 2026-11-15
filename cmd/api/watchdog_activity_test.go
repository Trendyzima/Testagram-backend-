package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func setWatchdogTestEnv(t *testing.T, name, value string) {
	t.Helper()
	previous, existed := os.LookupEnv(name)
	if err := os.Setenv(name, value); err != nil { t.Fatal(err) }
	t.Cleanup(func() {
		if existed { _ = os.Setenv(name, previous) } else { _ = os.Unsetenv(name) }
	})
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
		if body["eventType"] != "login" || len(body["idempotencyKey"]) != 48 || len(body) != 2 {
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
