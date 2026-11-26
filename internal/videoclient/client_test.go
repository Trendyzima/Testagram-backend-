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

func TestManifestRedirectIsRewrittenToBackendHLSRoute(t *testing.T) {
	const videoID = "00000000-0000-4000-8000-000000000001"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/videos/"+videoID+"/manifest" {
			t.Fatalf("unexpected manifest path %s", r.URL.Path)
		}
		w.Header().Set("Location", "/v1/videos/"+videoID+"/hls/master.m3u8?token=signed-token")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client, err := New(server.URL, nil)
	if err != nil { t.Fatal(err) }
	request := httptest.NewRequest(http.MethodGet, "/api/video/uploads/"+videoID+"/manifest", nil)
	recorder := httptest.NewRecorder()
	if err := client.Proxy(recorder, request, "/v1/videos/"+videoID+"/manifest", ""); err != nil {
		t.Fatal(err)
	}
	want := "/api/video/uploads/"+videoID+"/hls/master.m3u8?token=signed-token"
	if recorder.Code != http.StatusTemporaryRedirect || recorder.Header().Get("Location") != want {
		t.Fatalf("manifest response = %d Location %q, want %q", recorder.Code, recorder.Header().Get("Location"), want)
	}
}

func TestHLSProxyStreamsLargeSegmentsAndForwardsSignedToken(t *testing.T) {
	const token = "signed-playback-token"
	payload := strings.Repeat("x", 128<<10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/videos/00000000-0000-4000-8000-000000000001/hls/segment-001.ts" {
			t.Fatalf("unexpected HLS path %s", r.URL.Path)
		}
		if r.URL.Query().Get("token") != token {
			t.Fatalf("signed playback token was not forwarded: %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "video/mp2t")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, payload)
	}))
	defer server.Close()
	client, err := New(server.URL, nil)
	if err != nil { t.Fatal(err) }
	request := httptest.NewRequest(http.MethodGet, "/api/video/uploads/00000000-0000-4000-8000-000000000001/hls/segment-001.ts?token="+token, nil)
	recorder := httptest.NewRecorder()
	if err := client.Proxy(recorder, request, "/v1/videos/00000000-0000-4000-8000-000000000001/hls/segment-001.ts", ""); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "video/mp2t" || recorder.Body.Len() != len(payload) {
		t.Fatalf("HLS stream = status %d, content-type %q, bytes %d", recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.Len())
	}
}

func TestProxyForwardsPlaybackSessionWithoutForwardingClientAuthorizationForPublicActions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("public view unexpectedly forwarded Authorization: %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-Playback-Session") != "session-123" {
			t.Errorf("playback session missing: %q", r.Header.Get("X-Playback-Session"))
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	client, err := New(server.URL, nil)
	if err != nil { t.Fatal(err) }
	request := httptest.NewRequest(http.MethodPost, "/api/video/uploads/00000000-0000-4000-8000-000000000001/view", nil)
	request.Header.Set("X-Playback-Session", "session-123")
	recorder := httptest.NewRecorder()
	if err := client.Proxy(recorder, request, "/v1/videos/00000000-0000-4000-8000-000000000001/view", ""); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusAccepted { t.Fatalf("view proxy status = %d", recorder.Code) }
}

