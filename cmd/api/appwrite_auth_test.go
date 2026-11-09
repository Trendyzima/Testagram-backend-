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

func TestAppwriteProviderDoesNotFallbackToSupabase(t *testing.T) {
	t.Setenv("TESTAGRAM_AUTH_PROVIDER", "appwrite")
	t.Setenv("APPWRITE_ENDPOINT", "")
	t.Setenv("APPWRITE_PROJECT_ID", "")
	t.Setenv("SUPABASE_URL", "https://legacy-auth.example.test")
	t.Setenv("SUPABASE_PUBLISHABLE_KEY", "legacy-publishable-key")
	service := &service{upstream: &http.Client{Timeout: time.Second}}
	request := httptest.NewRequest(http.MethodPost, "/api/media/upload-intent", nil)
	request.Header.Set("Authorization", "Bearer appwrite-session-token")
	_, status, err := service.authenticatedMediaUser(request)
	if err == nil || status != http.StatusServiceUnavailable {
		t.Fatalf("status=%d err=%v; expected Appwrite configuration failure", status, err)
	}
}


func TestFetchAppwriteSessionReturnsCurrentMFAState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/account/sessions/current" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("X-Appwrite-Project") != "test-project" {
			t.Errorf("project header missing")
		}
		if r.Header.Get("X-Appwrite-JWT") != "session-jwt-value" {
			t.Errorf("JWT header missing")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"$id":"session_123","userId":"user_123","current":true,"factors":["password","totp"],"mfaUpdatedAt":"2026-11-09T10:00:00Z"}`))
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	session, status, err := fetchAppwriteSession(ctx, server.Client(), server.URL, "test-project", "session-jwt-value")
	if err != nil || status != http.StatusOK {
		t.Fatalf("session status=%d err=%v", status, err)
	}
	if session.ID != "session_123" || session.UserID != "user_123" || !session.Current || len(session.Factors) != 2 {
		t.Fatalf("unexpected session: %+v", session)
	}
}

func TestFetchAppwriteSessionDoesNotFollowRedirects(t *testing.T) {
	var targetHit bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetHit = true }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	_, status, err := fetchAppwriteSession(context.Background(), origin.Client(), origin.URL, "test-project", "session-jwt-value")
	if err == nil || status != http.StatusBadGateway {
		t.Fatalf("status=%d err=%v", status, err)
	}
	if targetHit {
		t.Fatal("Appwrite JWT was forwarded through a redirect")
	}
}

func TestAppwriteSessionRequiresRecentSecondFactor(t *testing.T) {
	now := time.Now().UTC()
	recent := now.Add(-2 * time.Minute).Format(time.RFC3339Nano)
	stale := now.Add(-6 * time.Minute).Format(time.RFC3339Nano)
	tests := []struct {
		name    string
		session appwriteSession
		want    bool
	}{
		{
			name: "recent TOTP",
			session: appwriteSession{Current: true, Factors: []string{"password", "totp"}, MFAUpdatedAt: recent},
			want: true,
		},
		{
			name: "stale MFA",
			session: appwriteSession{Current: true, Factors: []string{"password", "totp"}, MFAUpdatedAt: stale},
			want: false,
		},
		{
			name: "single factor",
			session: appwriteSession{Current: true, Factors: []string{"password"}, MFAUpdatedAt: recent},
			want: false,
		},
		{
			name: "not current session",
			session: appwriteSession{Current: false, Factors: []string{"password", "totp"}, MFAUpdatedAt: recent},
			want: false,
		},
		{
			name: "no second factor",
			session: appwriteSession{Current: true, Factors: []string{"password", "unknown"}, MFAUpdatedAt: recent},
			want: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := appwriteSessionHasRecentMFA(test.session, now); got != test.want {
				t.Fatalf("got %v, want %v", got, test.want)
			}
		})
	}
}
