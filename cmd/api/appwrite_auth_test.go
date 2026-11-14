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


func TestAuthenticatedAppwriteUserRejectsUnverifiedEmailBeforeIdentityLookup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/account" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"$id":"user_123","email":"person@example.test","emailVerification":false,"status":true}`))
	}))
	defer server.Close()

	t.Setenv("APPWRITE_ENDPOINT", server.URL)
	t.Setenv("APPWRITE_PROJECT_ID", "test-project")
	service := &service{upstream: server.Client()}
	request := httptest.NewRequest(http.MethodPost, "/api/social/posts", nil)
	_, status, err := service.authenticatedAppwriteUser(request, "valid-but-unverified-session")
	if err == nil || status != http.StatusForbidden {
		t.Fatalf("status=%d err=%v; expected verified-email authorization failure", status, err)
	}
	if !strings.Contains(err.Error(), "email verification is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}


func TestAppwriteDefaultUsernameIsStableAndValid(t *testing.T) {
	first := appwriteDefaultUsername("Trendy.User+test@example.test", "appwrite-user-123")
	second := appwriteDefaultUsername("Trendy.User+test@example.test", "appwrite-user-123")
	if first != second {
		t.Fatalf("username is not stable: %q != %q", first, second)
	}
	if !authUsernamePattern.MatchString(first) || len(first) > 30 {
		t.Fatalf("generated username is invalid: %q", first)
	}
	other := appwriteDefaultUsername("Trendy.User+test@example.test", "different-appwrite-user")
	if first == other {
		t.Fatalf("different Appwrite identities received the same generated username: %q", first)
	}
}

func TestAppwriteDefaultUsernameHandlesInvalidLocalPart(t *testing.T) {
	username := appwriteDefaultUsername("..@example.test", "appwrite-user-456")
	if !authUsernamePattern.MatchString(username) {
		t.Fatalf("generated username is invalid: %q", username)
	}
}


func TestFetchAppwriteAccountAllowsUnverifiedSessionForConsentOnlyFlow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"$id":"user_pending","email":"pending@example.test","emailVerification":false,"status":true}`))
	}))
	defer server.Close()
	account, status, err := fetchAppwriteAccount(context.Background(), server.Client(), server.URL, "test-project", "pending-session-token")
	if err != nil || status != http.StatusOK || account.EmailVerification {
		t.Fatalf("unverified consent session status=%d account=%+v err=%v", status, account, err)
	}
}

func TestAppwriteRegistrationConsentRequiresBearerSession(t *testing.T) {
	t.Setenv("TESTAGRAM_TERMS_VERSION", "2026-11-11")
	t.Setenv("TESTAGRAM_PRIVACY_VERSION", "2026-11-11")
	service := &service{}
	request := httptest.NewRequest(http.MethodPost, "/api/auth/registration-consent", strings.NewReader(`{"age_confirmed":true,"terms_accepted":true,"privacy_accepted":true,"terms_version":"2026-11-11","privacy_version":"2026-11-11"}`))
	response := httptest.NewRecorder()
	service.appwriteRegistrationConsent(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s; expected missing session rejection", response.Code, response.Body.String())
	}
}

func TestAppwriteRegistrationConsentRejectsUnacceptedPolicyBeforeDatabase(t *testing.T) {
	t.Setenv("TESTAGRAM_TERMS_VERSION", "2026-11-11")
	t.Setenv("TESTAGRAM_PRIVACY_VERSION", "2026-11-11")
	service := &service{}
	request := httptest.NewRequest(http.MethodPost, "/api/auth/registration-consent", strings.NewReader(`{"age_confirmed":true,"terms_accepted":false,"privacy_accepted":true,"terms_version":"2026-11-11","privacy_version":"2026-11-11"}`))
	request.Header.Set("Authorization", "Bearer valid-session-token")
	response := httptest.NewRecorder()
	service.appwriteRegistrationConsent(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s; expected explicit consent rejection", response.Code, response.Body.String())
	}
}
