package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
		{http.MethodPost, "/api/video/uploads/00000000-0000-4000-8000-000000000001/publish", ""},
		{http.MethodGet, "/api/video/feed", ""},
		{http.MethodGet, "/api/video/uploads/00000000-0000-4000-8000-000000000001/manifest", ""},
		{http.MethodGet, "/api/video/uploads/00000000-0000-4000-8000-000000000001/hls/master.m3u8", ""},
		{http.MethodPost, "/api/video/uploads/00000000-0000-4000-8000-000000000001/view", ""},
		{http.MethodPost, "/api/video/uploads/00000000-0000-4000-8000-000000000001/like", ""},
		{http.MethodGet, "/api/video/uploads/00000000-0000-4000-8000-000000000001/comments", ""},
		{http.MethodPost, "/api/video/uploads/00000000-0000-4000-8000-000000000001/comments", "{}"},
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

func TestVideoProxyRejectsMalformedHLSPath(t *testing.T) {
	handler := (&service{}).routes()
	for _, path := range []string{"/api/video/uploads/not-a-uuid/hls/master.m3u8", "/api/video/uploads/00000000-0000-4000-8000-000000000001/hls/../secret"} {
		recorder := httptest.NewRecorder(); handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusBadRequest { t.Fatalf("%s returned %d", path, recorder.Code) }
	}
}
func TestVideoProxyValidatesPlaybackSessionAndHLSQuery(t *testing.T) {
	handler := (&service{}).routes()
	id := "00000000-0000-4000-8000-000000000001"
	view := httptest.NewRequest(http.MethodPost, "/api/video/uploads/"+id+"/view", nil)
	view.Header.Set("Origin", "https://testagram.site")
	view.Header.Set("X-Playback-Session", "short")
	viewRecorder := httptest.NewRecorder(); handler.ServeHTTP(viewRecorder, view)
	if viewRecorder.Code != http.StatusBadRequest { t.Fatalf("invalid playback session returned %d", viewRecorder.Code) }
	hls := httptest.NewRequest(http.MethodGet, "/api/video/uploads/"+id+"/hls/master.m3u8", nil)
	hls.Header.Set("Origin", "https://testagram.site")
	hlsRecorder := httptest.NewRecorder(); handler.ServeHTTP(hlsRecorder, hls)
	if hlsRecorder.Code != http.StatusBadRequest { t.Fatalf("missing signed HLS token returned %d", hlsRecorder.Code) }
}