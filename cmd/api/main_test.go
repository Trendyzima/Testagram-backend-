package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/media"
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

func TestPublicReplicationProjectionValidatesIdentityAndRequiresDatabase(t *testing.T) {
	s := &service{}
	handler := s.routes()

	bad := httptest.NewRecorder()
	handler.ServeHTTP(bad, httptest.NewRequest(http.MethodGet, "/api/replication/public/unknown/post/post-123", nil))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("invalid projection origin status = %d, want %d", bad.Code, http.StatusBadRequest)
	}

	unavailable := httptest.NewRecorder()
	handler.ServeHTTP(unavailable, httptest.NewRequest(http.MethodGet, "/api/replication/public/xclone/post/post-123", nil))
	if unavailable.Code != http.StatusServiceUnavailable {
		t.Fatalf("projection read without database status = %d, want %d", unavailable.Code, http.StatusServiceUnavailable)
	}
	if got := normalizedRoute("/api/replication/public/xclone/post/post-123"); got != "/api/replication/public/{origin}/{entity_type}/{entity_id}" {
		t.Fatalf("projection route metric label = %q", got)
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

func TestCapabilityGatewayDoesNotProxyRetiredSupabase(t *testing.T) {
	t.Setenv("SUPABASE_URL", "")
	t.Setenv("SUPABASE_PUBLISHABLE_KEY", "")
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		http.Error(w, "retired upstream must not be called", http.StatusInternalServerError)
	}))
	defer upstream.Close()
	s := &service{upstream: upstream.Client()}
	r := httptest.NewRequest(http.MethodPost, "/api/capability", strings.NewReader(`{"capability":"testagram.posts.create","input":{"content":"hello"}}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer user-session-token")
	r.Header.Set("Origin", "https://testagram.site")
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusNotImplemented || !strings.Contains(w.Body.String(), "CAPABILITY_NOT_IMPLEMENTED") {
		t.Fatalf("unmigrated capability must fail closed: status=%d body=%s", w.Code, w.Body.String())
	}
	if upstreamCalls != 0 { t.Fatalf("retired upstream was called %d times", upstreamCalls) }
	if w.Header().Get("Access-Control-Allow-Origin") != "https://testagram.site" {
		t.Fatalf("unexpected CORS origin: %q", w.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestUnifiedSearchRequiresLinkedAppwriteIdentity(t *testing.T) {
	t.Setenv("APPWRITE_ENDPOINT", "")
	t.Setenv("APPWRITE_PROJECT_ID", "")
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		_, _ = w.Write([]byte(`{"users":[]}`))
	}))
	defer upstream.Close()
	s := &service{upstream: upstream.Client()}
	r := httptest.NewRequest(http.MethodPost, "/api/capability", strings.NewReader(`{"capability":"testagram.search.unified","input":{"q":"kenya","kind":"hashtags","limit":20,"offset":40,"sort":"latest"}}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer user-session-token")
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "AUTH_REQUIRED") {
		t.Fatalf("search without configured Appwrite identity must fail closed: status=%d body=%s", w.Code, w.Body.String())
	}
	if upstreamCalls != 0 { t.Fatalf("legacy upstream was called %d times", upstreamCalls) }
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
	client, err := platformclient.New(server.URL, "unit-test-secret-that-is-long-enough-for-api", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestUnmigratedPublicCapabilityDoesNotUseLegacyCache(t *testing.T) {
	cacheGets, upstreamCalls := 0, 0
	platform := newTestPlatformClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/rate-limit/check":
			_, _ = w.Write([]byte(`{"allowed":true,"count":1,"remaining":119,"retry_after_seconds":60}`))
		case "/v1/cache/get":
			cacheGets++
			_, _ = w.Write([]byte(`{"value":"legacy-cache-value"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { upstreamCalls++ }))
	defer upstream.Close()
	s := &service{upstream: upstream.Client(), platform: platform}
	r := httptest.NewRequest(http.MethodPost, "/api/capability", strings.NewReader(`{"capability":"testagram.news.trending","input":{}}`))
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusNotImplemented || !strings.Contains(w.Body.String(), "CAPABILITY_NOT_IMPLEMENTED") {
		t.Fatalf("unmigrated public capability must fail closed: status=%d body=%s", w.Code, w.Body.String())
	}
	if cacheGets != 0 || upstreamCalls != 0 { t.Fatalf("legacy cache/upstream used: cache=%d upstream=%d", cacheGets, upstreamCalls) }
}

func TestPlatformRateLimitStopsCapabilityDispatch(t *testing.T) {
	t.Setenv("SUPABASE_URL", "https://supabase.example")
	t.Setenv("SUPABASE_PUBLISHABLE_KEY", "publishable-test-key")
	platform := newTestPlatformClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/rate-limit/check" {
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprint(w, `{"allowed":false,"count":31,"remaining":0,"retry_after_seconds":17}`)
	}))
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { upstreamCalls++; _, _ = w.Write([]byte(`{}`)) }))
	defer upstream.Close()
	s := &service{upstream: upstream.Client(), platform: platform}
	r := httptest.NewRequest(http.MethodPost, "/api/capability", strings.NewReader(`{"capability":"testagram.posts.create","input":{"content":"hello"}}`))
	r.Header.Set("Authorization", "Bearer user-session-token")
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if w.Header().Get("Retry-After") != "17" {
		t.Fatalf("Retry-After=%q", w.Header().Get("Retry-After"))
	}
	if upstreamCalls != 0 {
		t.Fatalf("upstream called %d times after rate-limit denial", upstreamCalls)
	}
}

func TestAuthenticatedUnmigratedCapabilityNeverUsesSharedPublicCache(t *testing.T) {
	cacheGets, cacheSets := 0, 0
	platform := newTestPlatformClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/rate-limit/check":
			_, _ = w.Write([]byte(`{"allowed":true,"count":1,"remaining":119,"retry_after_seconds":60}`))
		case "/v1/cache/get":
			cacheGets++
			http.Error(w, "not found", http.StatusNotFound)
		case "/v1/cache/set":
			cacheSets++
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	s := &service{platform: platform}
	r := httptest.NewRequest(http.MethodPost, "/api/capability", strings.NewReader(`{"capability":"testagram.news.trending","input":{}}`))
	r.Header.Set("Authorization", "Bearer user-session-token")
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusNotImplemented { t.Fatalf("status=%d body=%s", w.Code, w.Body.String()) }
	if cacheGets != 0 || cacheSets != 0 { t.Fatalf("authenticated response touched shared cache: gets=%d sets=%d", cacheGets, cacheSets) }
}func testMediaSigner(t *testing.T) *media.Signer {
	t.Helper()
	signer, err := media.NewSigner(media.Config{
		Endpoint: "https://media.testagram.site", SigningHost: "object-storage:9000",
		AccessKey: "test-access", SecretKey: "test-secret", Region: "us-east-1",
		PrivateBucket: "private-media", PublicBucket: "public-media",
	})
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func testSupabaseAuthServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/v1/user" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("apikey") != "test-publishable-key" {
			http.Error(w, "bad key", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") != "Bearer valid-session-token" {
			http.Error(w, "bad token", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"id\":\"user-123\"}"))
	}))
	t.Cleanup(server.Close)
	t.Setenv("SUPABASE_URL", server.URL)
	t.Setenv("SUPABASE_PUBLISHABLE_KEY", "test-publishable-key")
	return server
}

func TestMediaUploadIntentRequiresAuthenticatedUser(t *testing.T) {
	testSupabaseAuthServer(t)
	s := &service{media: testMediaSigner(t)}
	r := httptest.NewRequest(http.MethodPost, "/api/media/upload-intent", strings.NewReader("{\"content_type\":\"image/png\",\"visibility\":\"private\",\"size_bytes\":100}"))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestMediaUploadIntentFailsClosedWithoutMetadataDatabase(t *testing.T) {
	upstream := testSupabaseAuthServer(t)
	s := &service{media: testMediaSigner(t), upstream: upstream.Client()}
	r := httptest.NewRequest(http.MethodPost, "/api/media/upload-intent", strings.NewReader("{\"content_type\":\"image/png\",\"visibility\":\"private\",\"size_bytes\":1024}"))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer valid-session-token")
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusServiceUnavailable, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "MEDIA_METADATA_UNAVAILABLE") {
		t.Fatalf("expected fail-closed metadata error, got %s", w.Body.String())
	}
}

func TestPrivateMediaDownloadURLFailsClosedWithoutMetadataDatabase(t *testing.T) {
	upstream := testSupabaseAuthServer(t)
	s := &service{media: testMediaSigner(t), upstream: upstream.Client()}
	r := httptest.NewRequest(http.MethodPost, "/api/media/private-download-url", strings.NewReader(`{"key":"users/user-123/0123456789abcdef0123456789abcdef"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer valid-session-token")
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusServiceUnavailable, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "MEDIA_METADATA_UNAVAILABLE") {
		t.Fatalf("expected fail-closed metadata error, got %s", w.Body.String())
	}
}

func TestMediaQuarantineRequiresOperatorToken(t *testing.T) {
	t.Setenv("MEDIA_MODERATION_TOKEN", "")
	r := httptest.NewRequest(http.MethodPost, "/internal/media/quarantine", strings.NewReader(`{"key":"users/user-123/0123456789abcdef0123456789abcdef","reason":"policy violation"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Testagram-Moderator-ID", "moderator-1")
	w := httptest.NewRecorder()
	(&service{}).routes().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusUnauthorized, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "MODERATION_UNAUTHORIZED") {
		t.Fatalf("expected moderation authorization error, got %s", w.Body.String())
	}
}

func TestMediaDeleteFailsClosedWithoutStorageOrMetadata(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/media/delete", strings.NewReader(`{"key":"users/user-123/0123456789abcdef0123456789abcdef"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	(&service{}).routes().ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusServiceUnavailable, w.Body.String())
	}
}

func TestMediaAttachFailsClosedWithoutMetadataDatabase(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/media/attach", strings.NewReader(`{"key":"users/user-123/0123456789abcdef0123456789abcdef","post_id":"post-123"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	(&service{}).routes().ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusServiceUnavailable, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "MEDIA_METADATA_UNAVAILABLE") {
		t.Fatalf("expected fail-closed metadata error, got %s", w.Body.String())
	}
}

func TestPublicMediaURLUsesCanonicalCDNPrefix(t *testing.T) {
	t.Setenv("MEDIA_PUBLIC_BASE_URL", "https://media.example.test/v1/")
	got := publicMediaURL("users/user-123/0123456789abcdef0123456789abcdef")
	want := "https://media.example.test/v1/users/user-123/0123456789abcdef0123456789abcdef"
	if got != want {
		t.Fatalf("publicMediaURL()=%q want=%q", got, want)
	}
	t.Setenv("MEDIA_PUBLIC_BASE_URL", "https://media.example.test/v1?cache=1")
	if got := publicMediaURL("users/user-123/0123456789abcdef0123456789abcdef"); got != "" {
		t.Fatalf("publicMediaURL accepted a query-bearing base URL: %q", got)
	}
}

func TestEnsureProductMediaRecordCreatesServerSideRecord(t *testing.T) {
	t.Setenv("SUPABASE_URL", "")
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer service-secret" || r.Header.Get("apikey") != "service-secret" {
			t.Errorf("missing server-only bearer/API key headers")
		}
		if r.URL.Path != "/rest/v1/media_assets" {
			t.Errorf("path=%q want=/rest/v1/media_assets", r.URL.Path)
		}
		switch r.Method {
		case http.MethodGet:
			if r.URL.Query().Get("storage_key") != "eq.users/user-123/0123456789abcdef0123456789abcdef" {
				t.Errorf("unexpected storage key filter: %s", r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("[]"))
		case http.MethodPost:
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode create payload: %v", err)
			}
			if body["owner_id"] != "user-123" || body["status"] != "uploaded" || body["original_name"] != "photo.png" || body["media_url"] != "https://media.example.test/public/users/user-123/0123456789abcdef0123456789abcdef" {
				t.Errorf("unexpected product media payload: %#v", body)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`[{"id":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","owner_id":"user-123","post_id":null,"status":"uploaded"}]`))
		default:
			t.Errorf("unexpected method %s", r.Method)
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()
	t.Setenv("SUPABASE_URL", server.URL)
	s := &service{upstream: server.Client()}
	got, err := s.ensureProductMediaRecord(context.Background(), "service-secret", "user-123",
		"users/user-123/0123456789abcdef0123456789abcdef", "photo.png", "image/png", 1234,
		"https://media.example.test/public/users/user-123/0123456789abcdef0123456789abcdef", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee" {
		t.Fatalf("external media ID=%q", got)
	}
}

func TestUserOwnsProductPostUsesAuthenticatedPostgRESTContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/rest/v1/posts" {
			t.Errorf("unexpected PostgREST request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer user-session-token" || r.Header.Get("apikey") != "publishable-key" {
			t.Errorf("post ownership lookup did not use the caller session")
		}
		if r.URL.Query().Get("id") != "eq.post-123" {
			t.Errorf("post filter=%q", r.URL.Query().Get("id"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"post-123","author_id":"user-123","user_id":"someone-else"}]`))
	}))
	defer server.Close()
	t.Setenv("SUPABASE_URL", server.URL)
	s := &service{upstream: server.Client()}
	owned, err := s.userOwnsProductPost(context.Background(), "Bearer user-session-token", "publishable-key", "user-123", "post-123")
	if err != nil {
		t.Fatal(err)
	}
	if !owned {
		t.Fatal("expected the authenticated post author to be allowed")
	}
	owned, err = s.userOwnsProductPost(context.Background(), "Bearer user-session-token", "publishable-key", "other-user", "post-123")
	if err != nil {
		t.Fatal(err)
	}
	if owned {
		t.Fatal("post must not be attachable by a non-owner")
	}
}

func TestSanitizeOriginalName(t *testing.T) {
	cases := []struct {
		name string
		want string
		bad  bool
	}{
		{name: "folder/render.png", want: "render.png"},
		{name: "  photo.webp  ", want: "photo.webp"},
		{name: "", want: "upload"},
		{name: "../", bad: true},
		{name: "bad\nname.jpg", bad: true},
		{name: "bad\x00name.jpg", bad: true},
	}
	for _, tc := range cases {
		got, err := sanitizeOriginalName(tc.name)
		if tc.bad {
			if err == nil {
				t.Errorf("sanitizeOriginalName(%q) = %q, want error", tc.name, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("sanitizeOriginalName(%q) = %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
}

func TestProductPostMediaTypeMatchesDatabaseConstraint(t *testing.T) {
	cases := []struct {
		contentType string
		want        string
		ok          bool
	}{
		{contentType: "image/jpeg", want: "image", ok: true},
		{contentType: "image/gif; charset=binary", want: "image", ok: true},
		{contentType: "video/mp4", want: "video", ok: true},
		{contentType: "audio/ogg", ok: false},
		{contentType: "application/pdf", ok: false},
		{contentType: "application/octet-stream", ok: false},
		{contentType: "", ok: false},
	}
	for _, tc := range cases {
		got, ok := productPostMediaType(tc.contentType)
		if got != tc.want || ok != tc.ok {
			t.Errorf("productPostMediaType(%q) = (%q, %v), want (%q, %v)", tc.contentType, got, ok, tc.want, tc.ok)
		}
	}
}

func TestProductPostMediaSizeMatchesDatabaseConstraint(t *testing.T) {
	if maxProductPostMediaBytes != 20<<20 {
		t.Fatalf("max product post media bytes=%d, want %d", maxProductPostMediaBytes, 20<<20)
	}
}

func TestDecodeSingleJSON(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "object", body: `{"capability":"testagram.health.read"}`},
		{name: "leading and trailing whitespace", body: " \n {\"ok\":true} \t\n"},
		{name: "second JSON value", body: `{"ok":true} {"admin":true}`, wantErr: true},
		{name: "trailing garbage", body: `{"ok":true} nope`, wantErr: true},
		{name: "empty", body: "", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got map[string]any
			err := decodeSingleJSON(strings.NewReader(tc.body), &got)
			if (err != nil) != tc.wantErr {
				t.Fatalf("decodeSingleJSON() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestMetricsExposeRouteStatusAndLatencyWithoutRawPathLabels(t *testing.T) {
	s := &service{}
	handler := s.routes()

	for _, path := range []string{"/readyz", "/unknown/private-user-id"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
	}

	r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	r = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	body := w.Body.String()
	for _, want := range []string{
		"testagram_http_requests_by_route_total{method=\"GET\",route=\"/readyz\",status=\"503\"} 1",
		"testagram_http_request_duration_seconds_bucket{method=\"GET\",route=\"/readyz\",le=\"+Inf\"} 1",
		"testagram_http_request_duration_seconds_count{method=\"GET\",route=\"/readyz\"} 1",
		"route=\"unmatched\",status=\"404\"",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q in:\n%s", want, body)
		}
	}
	if strings.Contains(body, "/unknown/private-user-id") {
		t.Fatal("metrics leaked raw request path into a label")
	}
}

func TestCapabilityMetricsGroupSearchWithoutHighCardinalityLabels(t *testing.T) {
	t.Setenv("APPWRITE_ENDPOINT", "")
	t.Setenv("APPWRITE_PROJECT_ID", "")
	s := &service{}
	request := httptest.NewRequest(http.MethodPost, "/api/capability", strings.NewReader(
		`{"capability":"testagram.search.unified","input":{"query":"timeout-test"}}`,
	))
	request.Header.Set("Authorization", "Bearer test-user-token")
	response := httptest.NewRecorder()
	s.routes().ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("search without Appwrite configuration status = %d, want %d; body=%s", response.Code, http.StatusBadGateway, response.Body.String())
	}
	metricsRequest := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsResponse := httptest.NewRecorder()
	s.routes().ServeHTTP(metricsResponse, metricsRequest)
	body := metricsResponse.Body.String()
	for _, want := range []string{
		`testagram_capability_requests_total{group="search",status="502"} 1`,
		`testagram_capability_request_duration_seconds_count{group="search"} 1`,
	} {
		if !strings.Contains(body, want) { t.Errorf("capability metrics missing %q in:\n%s", want, body) }
	}
	if strings.Contains(body, "testagram.search.unified") || strings.Contains(body, "timeout-test") {
		t.Fatal("metrics leaked raw capability names or user search terms")
	}
	if strings.Contains(body, "testagram_upstream_requests_total{group=\"search\"") {
		t.Fatal("unconfigured Appwrite identity unexpectedly reached an upstream service")
	}
}

func TestCapabilityGroupLabelsAreBounded(t *testing.T) {
	cases := map[string]string{
		"testagram.search.unified":           "search",
		"testagram.feed.home":                "feed",
		"testagram.media.upload":             "media",
		"testagram.auth.session":             "auth",
		"testagram.notifications.list":       "notifications",
		"testagram.federation.inbox":         "federation",
		"arbitrary-user-supplied-capability": "other",
	}
	for input, want := range cases {
		if got := capabilityGroup(input); got != want {
			t.Errorf("capabilityGroup(%q) = %q, want %q", input, got, want)
		}
	}
	if got := capabilityGroup("testagram.search.user-generated-suffix-123"); got != "search" {
		t.Fatalf("user-generated suffix must map to a fixed group, got %q", got)
	}
}

func TestMediaUploadGatewayPreflightAllowsConfiguredOrigin(t *testing.T) {
	r := httptest.NewRequest(http.MethodOptions, "/api/media/upload?key=users/user-123/0123456789abcdef0123456789abcdef", nil)
	r.Header.Set("Origin", "https://testagram.site")
	r.Header.Set("Access-Control-Request-Method", "PUT")
	r.Header.Set("Access-Control-Request-Headers", "authorization,content-type")
	w := httptest.NewRecorder()

	(&service{}).routes().ServeHTTP(w, r)

	if w.Code != http.StatusNoContent {
		t.Fatalf("upload preflight status = %d, want %d: %s", w.Code, http.StatusNoContent, w.Body.String())
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://testagram.site" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want configured origin", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, "PUT") || !strings.Contains(got, "OPTIONS") {
		t.Fatalf("Access-Control-Allow-Methods = %q, want PUT and OPTIONS", got)
	}
	allowedHeaders := strings.ToLower(w.Header().Get("Access-Control-Allow-Headers"))
	if !strings.Contains(allowedHeaders, "authorization") || !strings.Contains(allowedHeaders, "content-type") {
		t.Fatalf("Access-Control-Allow-Headers = %q, want authorization and content-type", allowedHeaders)
	}
	if !strings.Contains(allowedHeaders, "x-testagram-legacy-authorization") {
		t.Fatalf("Access-Control-Allow-Headers = %q, want the explicit legacy proof header", allowedHeaders)
	}
}

func TestUpstreamFailureOutcome(t *testing.T) {
	if got := upstreamFailureOutcome(context.DeadlineExceeded, context.DeadlineExceeded); got != "timeout" {
		t.Fatalf("deadline outcome = %q, want timeout", got)
	}
	if got := upstreamFailureOutcome(errors.New("connection reset"), nil); got != "transport_error" {
		t.Fatalf("transport outcome = %q, want transport_error", got)
	}
}

func TestUpstreamMetricLabelsAreBounded(t *testing.T) {
	s := &service{}
	s.recordUpstream("user-controlled-search-group", "user-controlled-outcome", 999, time.Second)
	body := s.metricsText()
	for _, want := range []string{
		`testagram_upstream_requests_total{group="other",outcome="transport_error"} 1`,
		`testagram_upstream_request_duration_seconds_count{group="other"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("upstream metrics missing %q in:\\n%s", want, body)
		}
	}
	if strings.Contains(body, "user-controlled-search-group") || strings.Contains(body, "user-controlled-outcome") {
		t.Fatal("upstream metrics must not expose arbitrary label values")
	}
}

func TestUnifiedSearchRPCBodyBoundsAndMapsInput(t *testing.T) {
	body, err := unifiedSearchRPCBody(map[string]any{
		"q": "kenya", "kind": "hashtags", "limit": float64(20), "offset": float64(40), "sort": "latest",
	})
	if err != nil {
		t.Fatalf("unifiedSearchRPCBody returned error: %v", err)
	}
	if body["p_q"] != "kenya" || body["p_kind"] != "hashtags" || body["p_sort"] != "latest" {
		t.Fatalf("unexpected mapped search parameters: %#v", body)
	}
	if body["p_limit"] != 20 || body["p_offset"] != 40 {
		t.Fatalf("unexpected bounded page values: %#v", body)
	}
}

func TestUnifiedSearchRPCBodyRejectsUnboundedInput(t *testing.T) {
	tests := []struct {
		name  string
		input map[string]any
	}{
		{name: "query too long", input: map[string]any{"q": strings.Repeat("x", 513)}},
		{name: "unsupported kind", input: map[string]any{"kind": "secrets"}},
		{name: "limit zero", input: map[string]any{"limit": float64(0)}},
		{name: "limit too large", input: map[string]any{"limit": float64(101)}},
		{name: "negative offset", input: map[string]any{"offset": float64(-1)}},
		{name: "offset too large", input: map[string]any{"offset": float64(100001)}},
		{name: "unsupported sort", input: map[string]any{"sort": "arbitrary-column"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := unifiedSearchRPCBody(tt.input); err == nil {
				t.Fatal("expected invalid search input to be rejected")
			}
		})
	}
}

func TestHomeFeedRequiresAuthenticatedSession(t *testing.T) {
	s := &service{}
	r := httptest.NewRequest(http.MethodGet, "/api/feed/home?limit=20", nil)
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("home feed status = %d, want %d: %s", w.Code, http.StatusUnauthorized, w.Body.String())
	}
}

func TestHomeFeedCursorRoundTripAndValidation(t *testing.T) {
	cursor := homeFeedCursor{CreatedAt: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC), ID: "123e4567-e89b-42d3-a456-426614174000"}
	decoded, err := decodeHomeFeedCursor(encodeHomeFeedCursor(cursor))
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.CreatedAt.Equal(cursor.CreatedAt) || decoded.ID != cursor.ID {
		t.Fatalf("cursor round trip mismatch: %#v", decoded)
	}
	if _, err := decodeHomeFeedCursor("not-a-cursor"); err == nil {
		t.Fatal("expected invalid cursor rejection")
	}
}

func TestPublicHomeFeedIsAnonymousButRequiresCanonicalStore(t *testing.T) {
	s := &service{}
	r := httptest.NewRequest(http.MethodGet, "/api/feed/public?limit=12", nil)
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("public feed without database status = %d, want %d: %s", w.Code, http.StatusServiceUnavailable, w.Body.String())
	}
	if strings.Contains(strings.ToLower(w.Body.String()), "authentication") {
		t.Fatalf("public feed unexpectedly requires authentication: %s", w.Body.String())
	}
}

func TestPublicHomeFeedRejectsInvalidLimit(t *testing.T) {
	s := &service{}
	r := httptest.NewRequest(http.MethodGet, "/api/feed/public?limit=1000", nil)
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("public feed invalid limit status = %d, want %d: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}
