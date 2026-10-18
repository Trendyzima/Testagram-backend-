package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealth(t *testing.T) {
	s := &Server{}
	r := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	s.health(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"ok"`) {
		t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
	}
}

func TestValidImageDigest(t *testing.T) { if !validImageDigest("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") { t.Fatal("expected valid digest") }; if validImageDigest("sha256:bad") { t.Fatal("expected invalid digest") }; if validImageDigest("sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA") { t.Fatal("expected lowercase-only digest") } }

func TestAllowedWorkloadImage(t *testing.T) {
	const approved = "ghcr.io/trendyzima/testagram-worker@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa, ghcr.io/trendyzima/testagram-api@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	tests := []struct {
		name string
		image string
		configured string
		want bool
	}{
		{name: "exact digest approved", image: "ghcr.io/trendyzima/testagram-worker@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", configured: approved, want: true},
		{name: "unapproved image denied", image: "ghcr.io/attacker/image@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", configured: approved, want: false},
		{name: "different digest denied", image: "ghcr.io/trendyzima/testagram-worker@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", configured: approved, want: false},
		{name: "empty policy denies all", image: "ghcr.io/trendyzima/testagram-worker@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", configured: "", want: false},
		{name: "empty image denied", image: "", configured: approved, want: false},
		{name: "whitespace around configured entry", image: "ghcr.io/trendyzima/testagram-api@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", configured: approved, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := allowedWorkloadImage(tt.image, tt.configured); got != tt.want {
				t.Fatalf("allowedWorkloadImage() = %v, want %v", got, tt.want)
			}
		})
	}
}
