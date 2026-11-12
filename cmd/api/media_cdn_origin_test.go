package main

import (
	"net/http"
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
