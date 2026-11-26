package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Trendyzima/Testagram-backend-/internal/videoclient"
)

func TestVideoProxyRoutesRequireNativeSession(t *testing.T) {
	s := &service{}
	handler := s.routes()
	requests := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/video/channels", ""},
		{http.MethodPost, "/api/video/channels", "{\"handle\":\"creator\",\"name\":\"Creator\"}"},
		{http.MethodPost, "/api/video/uploads", "multipart"},
		{http.MethodGet, "/api/video/uploads/00000000-0000-4000-8000-000000000001", ""},
		{http.MethodDelete, "/api/video/uploads/00000000-0000-4000-8000-000000000001", ""},
		{http.MethodPost, "/api/video/uploads/00000000-0000-4000-8000-000000000001/publish", ""},
		{http.MethodPost, "/api/video/uploads/00000000-0000-4000-8000-000000000001/like", ""},
		{http.MethodGet, "/api/video/uploads/00000000-0000-4000-8000-000000000001/comments", ""},
		{http.MethodPost, "/api/video/uploads/00000000-0000-4000-8000-000000000001/comments", "{\"body\":\"hello\"}"},
	}
	for _, tc := range requests {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			request.Header.Set("Origin", "https://testagram.site")
			if tc.method == http.MethodPost && tc.path == "/api/video/uploads" {
				request.ContentLength = int64(len(tc.body))
				request.Header.Set("Content-Type", "multipart/form-data; boundary=test")
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("unauthenticated video route status = %d, want %d: %s", recorder.Code, http.StatusUnauthorized, recorder.Body.String())
			}
		})
	}
}

func TestVideoProxyRejectsMalformedVideoIDs(t *testing.T) {
	s := &service{}
	handler := s.routes()
	requests := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/video/uploads/not-a-uuid"},
		{http.MethodDelete, "/api/video/uploads/not-a-uuid"},
		{http.MethodPost, "/api/video/uploads/not-a-uuid/publish"},
	}
	for _, tc := range requests {
		request := httptest.NewRequest(tc.method, tc.path, nil)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("malformed video route %s %s returned %d: %s", tc.method, tc.path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestVideoProxyOptionsUsesBackendCorsPreflight(t *testing.T) {
	request := httptest.NewRequest(http.MethodOptions, "/api/video/uploads", nil)
	request.Header.Set("Origin", "https://testagram.site")
	request.Header.Set("Access-Control-Request-Method", "POST")
	request.Header.Set("Access-Control-Request-Headers", "authorization,content-type")
	recorder := httptest.NewRecorder()
	(&service{}).routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("video upload preflight status = %d, want %d: %s", recorder.Code, http.StatusNoContent, recorder.Body.String())
	}

}

func TestVideoPlaybackRoutesRejectMalformedIDs(t *testing.T) {
	handler := (&service{}).routes()
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/video/uploads/not-a-uuid/view"},
		{http.MethodPost, "/api/video/uploads/not-a-uuid/like"},
		{http.MethodGet, "/api/video/uploads/not-a-uuid/comments"},
		{http.MethodGet, "/api/video/uploads/not-a-uuid/manifest"},
		{http.MethodGet, "/api/video/uploads/not-a-uuid/hls/master.m3u8?token=x"},
	} {
		request := httptest.NewRequest(tc.method, tc.path, nil)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("malformed playback route %s %s returned %d: %s", tc.method, tc.path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestVideoHLSRouteRequiresSignedPlaybackToken(t *testing.T) {
	handler := (&service{}).routes()
	request := httptest.NewRequest(http.MethodGet,
		"/api/video/uploads/00000000-0000-4000-8000-000000000001/hls/master.m3u8", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("unsigned HLS request status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
}

func TestVideoPublicPlaybackRoutesDoNotRequireUserBearer(t *testing.T) {
	handler := (&service{}).routes()
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/video/feed"},
		{http.MethodPost, "/api/video/uploads/00000000-0000-4000-8000-000000000001/view"},
		{http.MethodGet, "/api/video/uploads/00000000-0000-4000-8000-000000000001/manifest"},
	} {
		request := httptest.NewRequest(tc.method, tc.path, nil)
		if strings.Contains(tc.path, "/view") {
			request.Header.Set("X-Playback-Session", "playback-session-123")
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("public video route %s %s returned %d, want service-unavailable with unconfigured test service: %s",
				tc.method, tc.path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestVideoHLSRejectsUnsafeSegmentNames(t *testing.T) {
	handler := (&service{}).routes()
	for _, path := range []string{
		"/api/video/uploads/00000000-0000-4000-8000-000000000001/hls/master..m3u8?token=x",
		"/api/video/uploads/00000000-0000-4000-8000-000000000001/hls/%5csecret?token=x",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("unsafe HLS segment %s returned %d: %s", path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestVideoProxyValidatesPlaybackSessionAndHLSQuery(t *testing.T) {
	handler := (&service{}).routes()
	id := "00000000-0000-4000-8000-000000000001"
	view := httptest.NewRequest(http.MethodPost, "/api/video/uploads/"+id+"/view", nil)
	view.Header.Set("Origin", "https://testagram.site")
	view.Header.Set("X-Playback-Session", "short")
	viewRecorder := httptest.NewRecorder()
	handler.ServeHTTP(viewRecorder, view)
	if viewRecorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid playback session returned %d", viewRecorder.Code)
	}

	for _, path := range []string{
		"/api/video/uploads/" + id + "/hls/master.m3u8",
		"/api/video/uploads/" + id + "/hls/master.m3u8?token=a&token=b",
		"/api/video/uploads/" + id + "/hls/master.m3u8?token=" + strings.Repeat("x", 257),
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Origin", "https://testagram.site")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest && recorder.Code != http.StatusForbidden {
			t.Fatalf("invalid HLS query %s returned %d", path, recorder.Code)
		}
	}
}

func TestVideoHLSFilenameUsesSafeCharacterAllowlist(t *testing.T) {
	for _, file := range []string{"master.m3u8", "segment-001.ts", "variant_2.m3u8"} {
		if !validVideoHLSFilename(file) {
			t.Fatalf("valid HLS filename rejected: %q", file)
		}
	}
	for _, file := range []string{"", "..", "master..m3u8", "bad name.ts", "a/b.ts", "x\\\\secret.ts", "segment?token=x.ts"} {
		if validVideoHLSFilename(file) {
			t.Fatalf("unsafe HLS filename accepted: %q", file)
		}
	}
}

func TestPublicCaddyIngressDoesNotExposePrivateVideoOrigin(t *testing.T) {
	config, err := os.ReadFile("../../deploy/self-hosted/Caddyfile")
	if err != nil {
		t.Fatalf("read canonical Caddy ingress config: %v", err)
	}
	source := string(config)
	if !strings.Contains(source, "reverse_proxy api:8080") {
		t.Fatal("public API ingress must forward to the canonical Backend")
	}
	for _, forbidden := range []string{
		"reverse_proxy video:8790",
		"@video path /v1/feed",
		"handle @video",
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("public Caddy ingress exposes private video service through %q", forbidden)
		}
	}
}

func TestCaddyAndVideoUseDisjointNetworks(t *testing.T) {
	compose, err := os.ReadFile("../../deploy/self-hosted/docker-compose.yml")
	if err != nil {
		t.Fatalf("read canonical self-hosted compose file: %v", err)
	}
	source := string(compose)
	caddyStart := strings.Index(source, "\n  caddy:\n")
	caddyEnd := strings.Index(source[caddyStart+1:], "\n  node-exporter:")
	if caddyStart < 0 || caddyEnd < 0 {
		t.Fatal("Caddy service definition or boundary not found")
	}
	caddyService := source[caddyStart : caddyStart+1+caddyEnd]
	if !strings.Contains(caddyService, "networks: [edge, control-plane, platform]") ||
		strings.Contains(caddyService, "video-private") {
		t.Fatalf("Caddy must retain Federation platform access but never join video-private:\n%s", caddyService)
	}

	apiStart := strings.Index(source, "\n  api:\n")
	apiEnd := strings.Index(source[apiStart+1:], "\n  admin-console:")
	if apiStart < 0 || apiEnd < 0 {
		t.Fatal("Backend API service definition or boundary not found")
	}
	apiService := source[apiStart : apiStart+1+apiEnd]
	if !strings.Contains(apiService, "networks: [edge, data, monitoring, platform, egress, video-private]") {
		t.Fatalf("Backend API must retain existing networks and join video-private:\n%s", apiService)
	}
	if !strings.Contains(source, "video-private:\n    name: ${TESTAGRAM_VIDEO_NETWORK:-testagram-video-private}\n    internal: true") {
		t.Fatal("Backend Compose must create the dedicated internal video-private network")
	}

	script, err := os.ReadFile("../../scripts/start-instabox-deployment.sh")
	if err != nil {
		t.Fatalf("read Backend deployment script: %v", err)
	}
	if !strings.Contains(string(script), "write_dc_env_value TESTAGRAM_VIDEO_NETWORK") {
		t.Fatal("Backend deployment must pass the same video-private network name to the Data Centre overlay")
	}
}

func TestPublicVideoProxyDoesNotAppendJSONAfterInterruptedHLSStream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("short"))
	}))
	defer upstream.Close()

	client, err := videoclient.New(upstream.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := &service{videoService: client}
	request := httptest.NewRequest(http.MethodGet,
		"/api/video/uploads/00000000-0000-4000-8000-000000000001/hls/segment.ts?token=signed",
		nil)
	recorder := httptest.NewRecorder()
	s.videoPublicProxyRequest(recorder, request,
		"/v1/videos/00000000-0000-4000-8000-000000000001/hls/segment.ts")
	if recorder.Code != http.StatusOK || recorder.Body.String() != "short" {
		t.Fatalf("interrupted HLS response = %d %q", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "VIDEO_SERVICE_UNAVAILABLE") ||
		strings.Contains(recorder.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("gateway error corrupted committed media response: %#v %q", recorder.Header(), recorder.Body.String())
	}
}
