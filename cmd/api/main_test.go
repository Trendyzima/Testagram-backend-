package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthz(t *testing.T) {
	s := &service{}
	r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "ok" {
		t.Fatalf("healthz: status=%d body=%q", w.Code, w.Body.String())
	}
}

func TestReadyzFailsClosedWithoutDatabase(t *testing.T) {
	s := &service{}
	r := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}

func TestMetricsCountsRequests(t *testing.T) {
	s := &service{}
	handler := s.routes()
	for _, path := range []string{"/healthz", "/metrics"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
	}
	r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), "testagram_http_requests_total 3") {
		t.Fatalf("metrics did not report request count: %q", w.Body.String())
	}
}

func TestCapabilityRequiresBearerForPrivateCalls(t *testing.T) {
	t.Setenv("SUPABASE_URL", "https://supabase.example")
	t.Setenv("SUPABASE_PUBLISHABLE_KEY", "public-test-key")
	s := &service{}
	r := httptest.NewRequest(http.MethodPost, "/api/capability", strings.NewReader(`{"capability":"testagram.posts.create","input":{"content":"hello"}}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("private capability status = %d, want %d: %s", w.Code, http.StatusUnauthorized, w.Body.String())
	}
}

func TestCapabilityGatewayForwardsUserJWTAndPayload(t *testing.T) {
	t.Setenv("SUPABASE_URL", "")
	t.Setenv("SUPABASE_PUBLISHABLE_KEY", "test-publishable-key")
	observed := make(chan [3]bool, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authOK := r.Header.Get("Authorization") == "Bearer user-session-token"
		apiKeyOK := r.Header.Get("apikey") == "test-publishable-key"
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		observed <- [3]bool{authOK, apiKeyOK, body["p_capability"] == "testagram.posts.create"}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"post_id":"post-123","created":true}`))
	}))
	defer upstream.Close()
	t.Setenv("SUPABASE_URL", upstream.URL)

	s := &service{upstream: upstream.Client()}
	r := httptest.NewRequest(http.MethodPost, "/api/capability", strings.NewReader(`{"capability":"testagram.posts.create","input":{"content":"hello"}}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer user-session-token")
	r.Header.Set("Origin", "https://testagram.site")
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("gateway status = %d, want %d: %s", w.Code, http.StatusOK, w.Body.String())
	}
	seen := <-observed
	if !seen[0] || !seen[1] || !seen[2] {
		t.Fatalf("gateway did not forward contract: auth=%v apikey=%v payload=%v", seen[0], seen[1], seen[2])
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "https://testagram.site" {
		t.Fatalf("unexpected CORS origin: %q", w.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestCapabilityGatewayRejectsUntrustedOrigin(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/capability", strings.NewReader(`{"capability":"testagram.health.read","input":{}}`))
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	(&service{}).routes().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("untrusted origin status = %d, want %d", w.Code, http.StatusForbidden)
	}
}
