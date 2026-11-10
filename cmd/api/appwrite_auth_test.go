package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetchAppwriteAccountSendsProjectAndSessionJWT(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/account" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("X-Appwrite-Project") != "test-project" {
			t.Errorf("project header missing")
		}
		if r.Header.Get("X-Appwrite-JWT") != "session-jwt-value" {
			t.Errorf("JWT header missing")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"$id":"user_123","email":"person@example.test","emailVerification":true,"status":true}`))
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	account, status, err := fetchAppwriteAccount(ctx, server.Client(), server.URL, "test-project", "session-jwt-value")
	if err != nil || status != http.StatusOK {
		t.Fatalf("account status=%d err=%v", status, err)
	}
	if account.ID != "user_123" || account.Email != "person@example.test" || !account.EmailVerification || !account.Status {
		t.Fatalf("unexpected account: %+v", account)
	}
}

func TestFetchAppwriteAccountRejectsInvalidSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "unauthorized", http.StatusUnauthorized) }))
	defer server.Close()
	_, status, err := fetchAppwriteAccount(context.Background(), server.Client(), server.URL, "test-project", "bad-session-token")
	if err == nil || status != http.StatusUnauthorized {
		t.Fatalf("status=%d err=%v", status, err)
	}
}

func TestFetchAppwriteAccountDoesNotFollowRedirects(t *testing.T) {
	var targetHit bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetHit = true }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	_, status, err := fetchAppwriteAccount(context.Background(), origin.Client(), origin.URL, "test-project", "session-jwt-value")
	if err == nil || status != http.StatusBadGateway {
		t.Fatalf("status=%d err=%v", status, err)
	}
	if targetHit {
		t.Fatal("Appwrite JWT was forwarded through a redirect")
	}
}

func TestFetchAppwriteAccountRequiresSecureEndpoint(t *testing.T) {
	_, status, err := fetchAppwriteAccount(context.Background(), nil, "http://auth.example.test/v1", "project", "session-jwt-value")
	if err == nil || status != http.StatusServiceUnavailable {
		t.Fatalf("status=%d err=%v", status, err)
	}
}

func TestValidAppwriteUserID(t *testing.T) {
	for _, value := range []string{"user_123", "123e4567-e89b-42d3-a456-426614174000"} {
		if !validAppwriteUserID(value) {
			t.Errorf("expected valid ID %q", value)
		}
	}
	for _, value := range []string{"", " with-space", "line\nbreak", strings.Repeat("x", 37)} {
		if validAppwriteUserID(value) {
			t.Errorf("expected invalid ID %q", value)
		}
	}
}

func TestAppwriteAuthenticationFailsClosedWhenNotConfigured(t *testing.T) {
	t.Setenv("TESTAGRAM_AUTH_PROVIDER", "")
	t.Setenv("APPWRITE_ENDPOINT", "")
	t.Setenv("APPWRITE_PROJECT_ID", "")
	service := &service{upstream: &http.Client{Timeout: time.Second}}
	request := httptest.NewRequest(http.MethodPost, "/api/media/upload-intent", nil)
	request.Header.Set("Authorization", "Bearer appwrite-session-token")
	_, status, err := service.authenticatedMediaUser(request)
	if err == nil || status != http.StatusServiceUnavailable {
		t.Fatalf("status=%d err=%v; expected Appwrite configuration failure", status, err)
	}
}
