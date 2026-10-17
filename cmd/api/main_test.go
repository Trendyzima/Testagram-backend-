package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/platformclient"
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

func newTestPlatformClient(t *testing.T, handler http.Handler) *platformclient.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := platformclient.New(server.URL, "test-platform-token", time.Second)
	if err != nil { t.Fatal(err) }
	return client
}

func TestPublicCapabilityUsesSharedPlatformCache(t *testing.T) {
	t.Setenv("SUPABASE_URL", "https://supabase.example")
	t.Setenv("SUPABASE_PUBLISHABLE_KEY", "publishable-test-key")
	var cacheGets, upstreamCalls int
	platform := newTestPlatformClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-platform-token" { t.Errorf("missing platform auth") }
		switch r.URL.Path {
		case "/v1/rate-limit/check":
			_, _ = w.Write([]byte(`{"allowed":true,"count":1,"remaining":119,"retry_after_seconds":60}`))
		case "/v1/cache/get":
			cacheGets++
			_, _ = w.Write([]byte(`{"value":"{\"trending\":[\"cached-item\"]}"}`))
		default:
			http.NotFound(w,r)
		}
	}))
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request) {
		upstreamCalls++
		_, _ = w.Write([]byte(`{"trending":["upstream-item"]}`))
	}))
	defer upstream.Close()
	s := &service{upstream:upstream.Client(),platform:platform}
	r := httptest.NewRequest(http.MethodPost,"/api/capability",strings.NewReader(`{"capability":"testagram.news.trending","input":{}}`))
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w,r)
	if w.Code != http.StatusOK { t.Fatalf("status=%d body=%s",w.Code,w.Body.String()) }
	if cacheGets != 1 || upstreamCalls != 0 { t.Fatalf("cacheGets=%d upstreamCalls=%d",cacheGets,upstreamCalls) }
	if !strings.Contains(w.Body.String(),"cached-item") { t.Fatalf("cached response not returned: %s",w.Body.String()) }
	if !strings.Contains(w.Header().Get("Cache-Control"),"public") { t.Fatalf("expected public cache headers, got %q",w.Header().Get("Cache-Control")) }
}

func TestPlatformRateLimitStopsCapabilityDispatch(t *testing.T) {
	t.Setenv("SUPABASE_URL", "https://supabase.example")
	t.Setenv("SUPABASE_PUBLISHABLE_KEY", "publishable-test-key")
	platform := newTestPlatformClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/rate-limit/check" { http.NotFound(w,r); return }
		_, _ = fmt.Fprint(w,`{"allowed":false,"count":31,"remaining":0,"retry_after_seconds":17}`)
	}))
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request) { upstreamCalls++; _,_=w.Write([]byte(`{}`)) }))
	defer upstream.Close()
	s := &service{upstream:upstream.Client(),platform:platform}
	r := httptest.NewRequest(http.MethodPost,"/api/capability",strings.NewReader(`{"capability":"testagram.posts.create","input":{"content":"hello"}}`))
	r.Header.Set("Authorization","Bearer user-session-token")
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w,r)
	if w.Code != http.StatusTooManyRequests { t.Fatalf("status=%d body=%s",w.Code,w.Body.String()) }
	if w.Header().Get("Retry-After") != "17" { t.Fatalf("Retry-After=%q",w.Header().Get("Retry-After")) }
	if upstreamCalls != 0 { t.Fatalf("upstream called %d times after rate-limit denial",upstreamCalls) }
}

func TestAuthenticatedResponsesNeverUseSharedPublicCache(t *testing.T) {
	t.Setenv("SUPABASE_URL", "https://supabase.example")
	t.Setenv("SUPABASE_PUBLISHABLE_KEY", "publishable-test-key")
	var cacheGets, cacheSets int
	platform := newTestPlatformClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/rate-limit/check":
			_, _ = w.Write([]byte(`{"allowed":true,"count":1,"remaining":119,"retry_after_seconds":60}`))
		case "/v1/cache/get":
			cacheGets++; http.Error(w,"not found",http.StatusNotFound)
		case "/v1/cache/set":
			cacheSets++; w.WriteHeader(http.StatusNoContent)
		default: http.NotFound(w,r)
		}
	}))
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request) { _,_=w.Write([]byte(`{"private":"user-specific"}`)) }))
	defer upstream.Close()
	t.Setenv("SUPABASE_URL", upstream.URL)
	s := &service{upstream:upstream.Client(),platform:platform}
	r := httptest.NewRequest(http.MethodPost,"/api/capability",strings.NewReader(`{"capability":"testagram.news.trending","input":{}}`))
	r.Header.Set("Authorization","Bearer user-session-token")
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w,r)
	if w.Code != http.StatusOK { t.Fatalf("status=%d body=%s",w.Code,w.Body.String()) }
	if cacheGets != 0 || cacheSets != 0 { t.Fatalf("authenticated request touched shared cache: gets=%d sets=%d",cacheGets,cacheSets) }
	if w.Header().Get("Cache-Control") != "private, no-store" { t.Fatalf("unsafe cache header %q",w.Header().Get("Cache-Control")) }
}
