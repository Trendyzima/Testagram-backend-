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

func TestProxyRewritesOnlySameVideoHLSRedirects(t *testing.T) {
	const id = "00000000-0000-4000-8000-000000000001"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Location", "/v1/videos/"+id+"/hls/master.m3u8?token=signed"); w.WriteHeader(http.StatusTemporaryRedirect) }))
	defer server.Close()
	client, err := New(server.URL, nil); if err != nil { t.Fatal(err) }
	request := httptest.NewRequest(http.MethodGet, "/api/video/uploads/"+id+"/manifest", nil)
	recorder := httptest.NewRecorder()
	if err := client.Proxy(recorder, request, "/v1/videos/"+id+"/manifest", "native-token"); err != nil { t.Fatal(err) }
	if recorder.Code != http.StatusTemporaryRedirect { t.Fatalf("status = %d", recorder.Code) }
	want := "/api/video/uploads/"+id+"/hls/master.m3u8?token=signed"
	if got := recorder.Header().Get("Location"); got != want { t.Fatalf("Location = %q, want %q", got, want) }
}

func TestProxyStreamsHLSAndForwardsRangeAndPlaybackSession(t *testing.T) {
	const token = "native-user-session-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token { t.Errorf("authorization not forwarded") }
		if r.Header.Get("Range") != "bytes=0-99" { t.Errorf("Range = %q", r.Header.Get("Range")) }
		if r.Header.Get("X-Playback-Session") != "session-123" { t.Errorf("playback session not forwarded") }
		w.Header().Set("Content-Type", "video/mp2t"); w.Header().Set("Accept-Ranges", "bytes"); w.Header().Set("Content-Range", "bytes 0-99/100")
		w.WriteHeader(http.StatusPartialContent); _, _ = w.Write([]byte(strings.Repeat("x", 100)))
	}))
	defer server.Close()
	client, err := New(server.URL, nil); if err != nil { t.Fatal(err) }
	request := httptest.NewRequest(http.MethodGet, "/api/video/uploads/00000000-0000-4000-8000-000000000001/hls/segment-1.ts", nil)
	request.Header.Set("Range", "bytes=0-99"); request.Header.Set("X-Playback-Session", "session-123")
	recorder := httptest.NewRecorder()
	if err := client.Proxy(recorder, request, "/v1/videos/00000000-0000-4000-8000-000000000001/hls/segment-1.ts", token); err != nil { t.Fatal(err) }
	if recorder.Code != http.StatusPartialContent || recorder.Body.Len() != 100 { t.Fatalf("response = %d, %d bytes", recorder.Code, recorder.Body.Len()) }
	if recorder.Header().Get("Content-Range") != "bytes 0-99/100" { t.Fatalf("Content-Range = %q", recorder.Header().Get("Content-Range")) }
}

func TestPublicHLSLocationRejectsExternalAndCrossVideoRedirects(t *testing.T) {
	const id = "00000000-0000-4000-8000-000000000001"
	for _, location := range []string{"https://evil.example/v1/videos/"+id+"/hls/master.m3u8?token=x", "/v1/videos/00000000-0000-4000-8000-000000000002/hls/master.m3u8?token=x", "/v1/videos/"+id+"/hls/../secret?token=x"} {
		if _, ok := publicHLSLocation("/v1/videos/"+id+"/manifest", location); ok { t.Fatalf("unsafe location accepted: %s", location) }
	}
}