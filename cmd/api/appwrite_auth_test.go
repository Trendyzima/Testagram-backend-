package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
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
	request := httptest.NewRequest(http.MethodPost, "/api/auth/registration-consent", strings.NewReader(`{"adult_confirmed":true,"terms_accepted":true,"privacy_accepted":true,"terms_version":"2026-11-11","privacy_version":"2026-11-11"}`))
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
	request := httptest.NewRequest(http.MethodPost, "/api/auth/registration-consent", strings.NewReader(`{"adult_confirmed":true,"terms_accepted":false,"privacy_accepted":true,"terms_version":"2026-11-11","privacy_version":"2026-11-11"}`))
	request.Header.Set("Authorization", "Bearer valid-session-token")
	response := httptest.NewRecorder()
	service.appwriteRegistrationConsent(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s; expected explicit consent rejection", response.Code, response.Body.String())
	}
}


func TestAuthenticatedAppwriteUserRequiresConsentAndLinksCanonicalAccount(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect PostgreSQL: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping PostgreSQL: %v", err)
	}

	appwriteID := "ci-appwrite-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	email := appwriteID + "@example.test"
	var canonicalID string
	defer func() {
		var linked string
		_ = pool.QueryRow(context.Background(), `
			SELECT canonical_user_id::text
			FROM testagram_identity.appwrite_accounts
			WHERE appwrite_user_id=$1
		`, appwriteID).Scan(&linked)
		_, _ = pool.Exec(context.Background(), `DELETE FROM testagram_identity.appwrite_accounts WHERE appwrite_user_id=$1`, appwriteID)
		if linked != "" {
			_, _ = pool.Exec(context.Background(), `DELETE FROM testagram_local.accounts WHERE user_id=$1::uuid`, linked)
		}
		_, _ = pool.Exec(context.Background(), `DELETE FROM testagram_identity.appwrite_registration_consents WHERE appwrite_user_id=$1`, appwriteID)
	}()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/account" {
			t.Errorf("Appwrite path = %q", r.URL.Path)
		}
		if r.Header.Get("X-Appwrite-Project") != "integration-project" ||
			r.Header.Get("X-Appwrite-JWT") != "integration-session-jwt" {
			t.Errorf("Appwrite project/session headers were not forwarded")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"$id":"` + appwriteID + `","email":"` + email + `","name":"CI Appwrite User","emailVerification":true,"status":true}`))
	}))
	defer server.Close()
	t.Setenv("APPWRITE_ENDPOINT", server.URL)
	t.Setenv("APPWRITE_PROJECT_ID", "integration-project")
	t.Setenv("TESTAGRAM_TERMS_VERSION", "2026-11-11")
	t.Setenv("TESTAGRAM_PRIVACY_VERSION", "2026-11-11")

	svc := &service{db: pool, upstream: server.Client()}
	request := httptest.NewRequest(http.MethodGet, "/api/me/profile", nil)
	firstID, status, err := svc.authenticatedAppwriteUser(request, "integration-session-jwt")
	if status != http.StatusForbidden || err == nil || !strings.Contains(err.Error(), "registration consent") {
		t.Fatalf("unconsented account status=%d canonical=%q err=%v; expected consent gate", status, firstID, err)
	}

	for _, consent := range []struct{ kind, version string }{
		{"age_confirmation", "age-v1"},
		{"terms", "2026-11-11"},
		{"privacy", "2026-11-11"},
	} {
		_, err := pool.Exec(ctx, `
			INSERT INTO testagram_identity.appwrite_registration_consents
			  (appwrite_user_id,email,consent_type,policy_version,accepted_at,request_id)
			VALUES($1,$2,$3,$4,now(),'ci-appwrite-flow')
		`, appwriteID, email, consent.kind, consent.version)
		if err != nil {
			t.Fatalf("insert %s consent: %v", consent.kind, err)
		}
	}

	firstID, status, err = svc.authenticatedAppwriteUser(request, "integration-session-jwt")
	if err != nil || status != http.StatusOK || firstID == "" {
		t.Fatalf("consented account provisioning status=%d canonical=%q err=%v", status, firstID, err)
	}
	secondID, status, err := svc.authenticatedAppwriteUser(request, "integration-session-jwt")
	if err != nil || status != http.StatusOK || secondID != firstID {
		t.Fatalf("repeat provisioning must be idempotent: first=%q second=%q status=%d err=%v", firstID, secondID, status, err)
	}
	canonicalID = firstID
	var mappings int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM testagram_identity.appwrite_accounts WHERE appwrite_user_id=$1
	`, appwriteID).Scan(&mappings); err != nil || mappings != 1 {
		t.Fatalf("canonical mapping count=%d err=%v", mappings, err)
	}
}
