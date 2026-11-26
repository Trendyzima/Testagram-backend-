package videoclient

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewRequiresPrivateVideoServiceHost(t *testing.T) {
	for _, raw := range []string{
		"https://video.example.com",
		"http://example.com",
		"http://video:8790/path",
		"http://user:pass@video:8790",
	} {
		if _, err := New(raw, nil); err == nil {
			t.Fatalf("unsafe Data Centre URL accepted: %s", raw)
		}
	}
	for _, raw := range []string{
		"http://video:8790",
		"http://video.internal:8790",
		"http://127.0.0.1:8790",
	} {
		if _, err := New(raw, nil); err != nil {
			t.Fatalf("private Data Centre URL rejected: %s: %v", raw, err)
		}
	}
}

func TestProxyForwardsValidatedNativeIdentityAndStreamsMultipartBody(t *testing.T) {
	const token = "native-user-session-token"
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/videos" {
			t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("native user token was not forwarded: %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-Testagram-API-Version") != "1" {
			t.Errorf("missing API version header: %q", r.Header.Get("X-Testagram-API-Version"))
		}
		if r.Header.Get("Content-Type") != "multipart/form-data; boundary=test-boundary" {
			t.Errorf("content type not preserved: %q", r.Header.Get("Content-Type"))
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read proxied body: %v", err)
		}
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("{\"id\":\"video-1\",\"status\":\"queued\"}"))
	}))
	defer server.Close()

	client, err := New(server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/video/uploads", strings.NewReader("multipart-video-bytes"))
	request.ContentLength = int64(len("multipart-video-bytes"))
	request.Header.Set("Content-Type", "multipart/form-data; boundary=test-boundary")
	recorder := httptest.NewRecorder()
	if err := client.Proxy(recorder, request, "/v1/videos", token); err != nil {
		t.Fatal(err)
	}
	if gotBody != "multipart-video-bytes" {
		t.Fatalf("upstream body = %q", gotBody)
	}
	if recorder.Code != http.StatusAccepted || !strings.Contains(recorder.Body.String(), "\"video-1\"") {
		t.Fatalf("proxy response = %d %q", recorder.Code, recorder.Body.String())
	}
}

func TestProxyRejectsUnsafePathAndRedirect(t *testing.T) {
	client, err := New("http://video:8790", nil)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/video/channels", nil)
	if err := client.Proxy(httptest.NewRecorder(), request, "https://evil.example/v1/channels", "user-token"); err == nil {
		t.Fatal("absolute upstream path was accepted")
	}
	if err := client.Proxy(httptest.NewRecorder(), request, "/v1/../admin", "user-token"); err == nil {
		t.Fatal("path traversal was accepted")
	}

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://evil.example")
		w.WriteHeader(http.StatusFound)
	}))
	defer redirect.Close()
	redirectClient, err := New(redirect.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := redirectClient.Proxy(httptest.NewRecorder(), request, "/v1/channels", "user-token"); err == nil {
		t.Fatal("upstream redirect was accepted")
	}
}

func TestProxyFailsClosedWhenNotConfigured(t *testing.T) {
	var client *Client
	request := httptest.NewRequest(http.MethodGet, "/api/video/channels", nil)
	if err := client.Proxy(httptest.NewRecorder(), request, "/v1/channels", "user-token"); err == nil {
		t.Fatal("unconfigured video proxy succeeded")
	}
}

func TestPublicFeedAndPlaybackProxyStayOnPrivateOrigin(t *testing.T) {
	const videoID = "00000000-0000-4000-8000-000000000001"
	const playbackToken = "signed-playback-token.1799999999"
	segment := strings.Repeat("segment-data-", 30000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("public playback route unexpectedly received Authorization: %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/feed":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("[]"))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/videos/"+videoID+"/manifest":
			w.Header().Set("Location", "/v1/videos/"+videoID+"/hls/master.m3u8?token="+playbackToken)
			w.WriteHeader(http.StatusTemporaryRedirect)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/videos/"+videoID+"/hls/segment.ts":
			if r.URL.Query().Get("token") != playbackToken {
				t.Errorf("playback token not forwarded: %q", r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "video/mp2t")
			w.Header().Set("Cache-Control", "private, max-age=300")
			_, _ = io.WriteString(w, segment)
		default:
			t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.String())
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := New(server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}

	feedRequest := httptest.NewRequest(http.MethodGet, "/api/video/feed", nil)
	feedResponse := httptest.NewRecorder()
	if err := client.Proxy(feedResponse, feedRequest, "/v1/feed", ""); err != nil {
		t.Fatal(err)
	}
	if feedResponse.Code != http.StatusOK || feedResponse.Body.String() != "[]" {
		t.Fatalf("feed proxy response = %d %q", feedResponse.Code, feedResponse.Body.String())
	}

	manifestRequest := httptest.NewRequest(http.MethodGet, "/api/video/uploads/"+videoID+"/manifest", nil)
	manifestResponse := httptest.NewRecorder()
	if err := client.Proxy(manifestResponse, manifestRequest, "/v1/videos/"+videoID+"/manifest", ""); err != nil {
		t.Fatal(err)
	}
	wantLocation := "/api/video/uploads/"+videoID+"/hls/master.m3u8?token="+playbackToken
	if manifestResponse.Code != http.StatusTemporaryRedirect || manifestResponse.Header().Get("Location") != wantLocation {
		t.Fatalf("manifest redirect = %d %q, want %q", manifestResponse.Code, manifestResponse.Header().Get("Location"), wantLocation)
	}

	hlsRequest := httptest.NewRequest(http.MethodGet, "/api/video/uploads/"+videoID+"/hls/segment.ts?token="+playbackToken, nil)
	hlsResponse := httptest.NewRecorder()
	if err := client.Proxy(hlsResponse, hlsRequest, "/v1/videos/"+videoID+"/hls/segment.ts?token="+playbackToken, ""); err != nil {
		t.Fatal(err)
	}
	if hlsResponse.Code != http.StatusOK || hlsResponse.Body.String() != segment {
		t.Fatalf("HLS response = %d, body bytes %d", hlsResponse.Code, hlsResponse.Body.Len())
	}
	if hlsResponse.Header().Get("Content-Type") != "video/mp2t" {
		t.Fatalf("HLS content type = %q", hlsResponse.Header().Get("Content-Type"))
	}
}

func TestProxyDoesNotAllowAnonymousPrivateVideoOperations(t *testing.T) {
	client, err := New("http://video:8790", nil)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/video/uploads", nil)
	for _, path := range []string{
		"/v1/channels",
		"/v1/videos",
		"/v1/videos/00000000-0000-4000-8000-000000000001/like",
		"/v1/videos/00000000-0000-4000-8000-000000000001/comments",
	} {
		if err := client.Proxy(httptest.NewRecorder(), request, path, ""); err == nil {
			t.Fatalf("anonymous private video operation accepted: %s", path)
		}
	}
}

func TestManifestProxyRejectsExternalAndUnexpectedRedirects(t *testing.T) {
	for _, location := range []string{
		"https://evil.example/steal",
		"/v1/videos/00000000-0000-4000-8000-000000000002/hls/master.m3u8?token=abc",
		"/v1/videos/00000000-0000-4000-8000-000000000001/hls/master.m3u8",
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", location)
			w.WriteHeader(http.StatusTemporaryRedirect)
		}))
		client, err := New(server.URL, nil)
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodGet, "/api/video/uploads/00000000-0000-4000-8000-000000000001/manifest", nil)
		if err := client.Proxy(httptest.NewRecorder(), request, "/v1/videos/00000000-0000-4000-8000-000000000001/manifest", ""); err == nil {
			server.Close()
			t.Fatalf("unsafe manifest redirect accepted: %s", location)
		}
		server.Close()
	}
}
