package main

import (
	"net/http"
	"net/http/httptest"
	"fmt"
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


func TestValidQueuePayloadBounds(t *testing.T) {
	tests := []struct {
		name string
		payload map[string]string
		want bool
	}{
		{name: "valid", payload: map[string]string{"job_id":"123","kind":"federation"}, want: true},
		{name: "empty", payload: map[string]string{}, want: false},
		{name: "too many fields", payload: func() map[string]string { m:=map[string]string{}; for i:=0;i<65;i++ { m[fmt.Sprintf("k%d",i)]="v" }; return m }(), want: false},
		{name: "payload too large", payload: map[string]string{"body":strings.Repeat("x",256*1024)}, want: false},
	}
	for _,tt:=range tests {
		t.Run(tt.name,func(t *testing.T) {
			if got:=validQueuePayload(tt.payload); got!=tt.want { t.Fatalf("validQueuePayload()=%v, want %v",got,tt.want) }
		})
	}
}


func TestDecodeJSONRejectsTrailingData(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "valid object", body: `{"key":"value"}`, want: true},
		{name: "second JSON value", body: `{"key":"value"} {"key":"attacker"}`, want: false},
		{name: "trailing garbage", body: `{"key":"value"} garbage`, want: false},
		{name: "empty body", body: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			var payload struct {
				Key string `json:"key"`
			}
			if got := decodeJSON(rec, req, &payload); got != tt.want {
				t.Fatalf("decodeJSON() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDecodeJSONRejectsUnknownFields(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"key":"value","unexpected":true}`))
	rec := httptest.NewRecorder()
	var payload struct {
		Key string `json:"key"`
	}
	if decodeJSON(rec, req, &payload) {
		t.Fatal("decodeJSON accepted an unknown field")
	}
}
