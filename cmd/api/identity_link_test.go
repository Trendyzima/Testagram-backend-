package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBearerToken(t *testing.T) {
	for _, value := range []string{"Bearer abcdefgh", "bearer 12345678"} {
		if token, ok := bearerToken(value); !ok || len(token) < 8 {
			t.Errorf("expected valid bearer token for %q", value)
		}
	}
	for _, value := range []string{"", "abcdefgh", "Bearer", "Bearer short", "Bearer a b", "Bearer " + strings.Repeat("x", 8193), "Bearer abcdefgh\n"} {
		if _, ok := bearerToken(value); ok {
			t.Errorf("expected invalid bearer token for %q", value)
		}
	}
}

func TestFetchLegacySupabaseUserVerifiesSessionAtConfiguredOrigin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/v1/user" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("apikey") != "public-key" {
			t.Error("publishable key header missing")
		}
		if r.Header.Get("Authorization") != "Bearer legacy-session-token" {
			t.Error("legacy session header missing")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"id\":\"123E4567-E89B-42D3-A456-426614174000\",\"email\":\"legacy@example.test\"}"))
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	user, status, err := fetchLegacySupabaseUser(ctx, server.Client(), server.URL, "public-key", "legacy-session-token")
	if err != nil || status != http.StatusOK {
		t.Fatalf("status=%d err=%v", status, err)
	}
	if user.ID != "123e4567-e89b-42d3-a456-426614174000" {
		t.Fatalf("legacy UUID was not normalized: %q", user.ID)
	}
}

func TestFetchLegacySupabaseUserRejectsMalformedIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{\"id\":\"not-a-uuid\"}"))
	}))
	defer server.Close()
	_, status, err := fetchLegacySupabaseUser(context.Background(), server.Client(), server.URL, "public-key", "legacy-session-token")
	if err == nil || status != http.StatusBadGateway {
		t.Fatalf("status=%d err=%v; expected malformed identity rejection", status, err)
	}
}

func TestFetchLegacySupabaseUserDoesNotForwardTokenOnRedirect(t *testing.T) {
	targetHit := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetHit = true }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	_, status, err := fetchLegacySupabaseUser(context.Background(), origin.Client(), origin.URL, "public-key", "legacy-session-token")
	if err == nil || status != http.StatusBadGateway {
		t.Fatalf("status=%d err=%v; expected redirect rejection", status, err)
	}
	if targetHit {
		t.Fatal("legacy bearer token was forwarded through a redirect")
	}
}

func TestFetchLegacySupabaseUserRequiresSecureEndpoint(t *testing.T) {
	_, status, err := fetchLegacySupabaseUser(context.Background(), nil, "http://auth.example.test", "public-key", "legacy-session-token")
	if err == nil || status != http.StatusServiceUnavailable {
		t.Fatalf("status=%d err=%v", status, err)
	}
}
