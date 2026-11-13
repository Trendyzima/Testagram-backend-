package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCDNOriginObjectSize(t *testing.T) {
	tests := []struct {
		name        string
		contentLen  int64
		contentRange string
		want        int64
		wantErr     bool
	}{
		{name: "full object", contentLen: 4096, want: 4096},
		{name: "partial response total", contentLen: 1024, contentRange: "bytes 0-1023/8192", want: 8192},
		{name: "unsatisfied range total", contentLen: 0, contentRange: "bytes */8192", want: 8192},
		{name: "unknown length", contentLen: -1, wantErr: true},
		{name: "malformed range", contentLen: 10, contentRange: "bytes 0-9/unknown", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{ContentLength: tt.contentLen, Header: make(http.Header)}
			if tt.contentRange != "" {
				resp.Header.Set("Content-Range", tt.contentRange)
			}
			got, err := cdnOriginObjectSize(resp)
			if (err != nil) != tt.wantErr {
				t.Fatalf("cdnOriginObjectSize() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Fatalf("cdnOriginObjectSize() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestCDNOriginRequestForwardsRangeValidators(t *testing.T) {
	source := httptest.NewRequest(http.MethodGet, "/internal/cdn/v1/users/u1/video.mp4", nil)
	source.Header.Set("Range", "bytes=10-19")
	source.Header.Set("If-Range", `"media-v1"`)
	source.Header.Set("If-None-Match", `"media-v1"`)
	destination := httptest.NewRequest(http.MethodGet, "https://storage.example/object", nil)

	copyCDNOriginRequestHeaders(source, destination)

	for name, want := range map[string]string{
		"Range": "bytes=10-19",
		"If-Range": `"media-v1"`,
		"If-None-Match": `"media-v1"`,
	} {
		if got := destination.Header.Get(name); got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestWriteCDNOriginResponsePreservesPartialRange(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusPartialContent,
		Header: http.Header{
			"Content-Type": []string{"video/mp4"},
			"Content-Length": []string{"5"},
			"Content-Range": []string{"bytes 10-14/100"},
			"Accept-Ranges": []string{"bytes"},
			"ETag": []string{`"media-v1"`},
		},
		Body: io.NopCloser(strings.NewReader("abcde")),
	}
	req := httptest.NewRequest(http.MethodGet, "/media/video", nil)
	rec := httptest.NewRecorder()

	writeCDNOriginResponse(rec, req, resp, "users/u1/video.mp4", "private")

	if rec.Code != http.StatusPartialContent || rec.Body.String() != "abcde" {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	for name, want := range map[string]string{
		"Content-Range": "bytes 10-14/100",
		"Accept-Ranges": "bytes",
		"Content-Length": "5",
		"Cache-Control": "private, no-store",
	} {
		if got := rec.Header().Get(name); got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestWriteCDNOriginResponseHEADHasHeadersButNoBody(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"video/mp4"},
			"Content-Length": []string{"1048576"},
			"Accept-Ranges": []string{"bytes"},
		},
		Body: io.NopCloser(strings.NewReader("must-not-be-written")),
	}
	req := httptest.NewRequest(http.MethodHead, "/media/video", nil)
	rec := httptest.NewRecorder()

	writeCDNOriginResponse(rec, req, resp, "users/u1/video.mp4", "private")

	if rec.Code != http.StatusOK || rec.Body.Len() != 0 || rec.Header().Get("Content-Length") != "1048576" {
		t.Fatalf("HEAD status=%d bytes=%d headers=%+v", rec.Code, rec.Body.Len(), rec.Header())
	}
}

func TestWriteCDNOriginResponsePublicUsesSharedCachePolicy(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{"Content-Type": []string{"image/webp"}, "Content-Length": []string{"3"}},
		Body: io.NopCloser(strings.NewReader("img")),
	}
	req := httptest.NewRequest(http.MethodGet, "/media/image", nil)
	rec := httptest.NewRecorder()
	writeCDNOriginResponse(rec, req, resp, "users/u1/image", "public")
	if rec.Header().Get("X-Testagram-Visibility") != "public" || rec.Header().Get("Cache-Control") != "public, max-age=0, s-maxage=300" {
		t.Fatalf("public origin cache policy is unsafe: %v", rec.Header())
	}
	if rec.Body.String() != "img" { t.Fatalf("body = %q", rec.Body.String()) }
}
