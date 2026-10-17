package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidKeyBoundsAndCharacters(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want bool
	}{
		{name: "simple", key: "feed:user-123", want: true},
		{name: "dot underscore", key: "cache.v1_item", want: true},
		{name: "empty", key: "", want: false},
		{name: "space", key: "bad key", want: false},
		{name: "slash", key: "user/123", want: false},
		{name: "too long", key: strings.Repeat("a", 161), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validKey(tt.key); got != tt.want {
				t.Fatalf("validKey(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestAuthorizeRequiresExactBearerToken(t *testing.T) {
	s := &server{internal: "expected-secret"}
	tests := []struct {
		name   string
		header string
		want   bool
		status int
	}{
		{name: "missing", header: "", want: false, status: http.StatusUnauthorized},
		{name: "wrong scheme", header: "Basic expected-secret", want: false, status: http.StatusUnauthorized},
		{name: "wrong token", header: "Bearer wrong", want: false, status: http.StatusUnauthorized},
		{name: "valid", header: "Bearer expected-secret", want: true, status: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/cache/get?key=x", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rec := httptest.NewRecorder()
			got := s.authorize(rec, req)
			if got != tt.want {
				t.Fatalf("authorize() = %v, want %v", got, tt.want)
			}
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d", rec.Code, tt.status)
			}
		})
	}
}
