package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNativeIdentityProvisioningTokenAndRedeemIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL identity provisioning integration")
	}
	liveAppwrite := strings.EqualFold(strings.TrimSpace(os.Getenv("TESTAGRAM_LIVE_APPWRITE_IDENTITY")), "true")
	requiredEnv := func(name string) string {
		value := strings.TrimSpace(os.Getenv(name))
		if value == "" {
			t.Fatalf("%s is required for live Appwrite identity acceptance", name)
		}
		return value
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var applicationID, applicationEmail, username, applicationStatus string
	var applicationToken, appwriteUserID, appwriteEmail, appwriteJWT, appwriteProjectID string
	var emailVerified bool
	var appwriteServer *httptest.Server
	var upstream *http.Client

	if liveAppwrite {
		applicationID = requiredEnv("APPWRITE_STAGING_APPLICATION_ID")
		applicationToken = requiredEnv("APPWRITE_STAGING_APPLICATION_TOKEN")
		appwriteUserID = requiredEnv("APPWRITE_STAGING_USER_ID")
		appwriteEmail = strings.ToLower(requiredEnv("APPWRITE_STAGING_USER_EMAIL"))
		appwriteJWT = requiredEnv("APPWRITE_STAGING_USER_JWT")
		appwriteProjectID = requiredEnv("APPWRITE_PROJECT_ID")
		endpoint := requiredEnv("APPWRITE_ENDPOINT")
		if !strings.HasPrefix(endpoint, "https://") || !strings.HasSuffix(strings.TrimRight(endpoint, "/"), "/v1") {
			t.Fatal("APPWRITE_ENDPOINT must be an HTTPS Appwrite /v1 endpoint")
		}
		if len(applicationToken) < 32 || len(appwriteJWT) < 32 || !validAppwriteUserID(appwriteUserID) {
			t.Fatal("live Appwrite identity credentials are incomplete")
		}
		t.Setenv("APPWRITE_ENDPOINT", strings.TrimRight(endpoint, "/"))
		t.Setenv("APPWRITE_PROJECT_ID", appwriteProjectID)
		emailVerified = true
		upstream = &http.Client{Timeout: 15 * time.Second}

		var requestStatus string
		var reviewCount, approvalLedgerCount, approvalTokenCount int
		err := db.QueryRow(ctx, `
			SELECT a.email, a.username, a.status, sar.status,
			       (SELECT count(DISTINCT r.reviewer_appwrite_user_id) FROM testagram_identity.identity_application_reviews r
			        WHERE r.application_id=a.id AND r.decision='approved'),
			       (SELECT count(DISTINCT sa.approver_appwrite_user_id) FROM testagram_auth.sensitive_action_approvals sa
			        WHERE sa.request_id=sar.id AND sa.decision='approved'),
			       (SELECT count(*) FROM testagram_identity.identity_approval_tokens tok
			        WHERE tok.application_id=a.id AND tok.consumed_at IS NULL
			          AND tok.first_approver_appwrite_user_id <> tok.second_approver_appwrite_user_id)
			FROM testagram_identity.identity_applications a
			JOIN testagram_auth.sensitive_action_requests sar
			  ON sar.id=a.sensitive_action_request_id
			WHERE a.id=$1::uuid
		`, applicationID).Scan(&applicationEmail, &username, &applicationStatus, &requestStatus, &reviewCount, &approvalLedgerCount, &approvalTokenCount)
		if err != nil {
			t.Fatalf("load dedicated pre-approved staging application: %v", err)
		}
		if applicationStatus != "approved" || requestStatus != "approved" || reviewCount != 2 || approvalLedgerCount != 2 || approvalTokenCount != 1 {
			t.Fatalf("staging application is not ready for provisioning: application=%q request=%q reviews=%d approval_ledger=%d unconsumed_tokens=%d; want approved/approved/2/2/1",
				applicationStatus, requestStatus, reviewCount, approvalLedgerCount, approvalTokenCount)
		}
		if applicationEmail != appwriteEmail {
			t.Fatalf("staging Appwrite email %q does not match approved application email %q", appwriteEmail, applicationEmail)
		}
	} else {
		if err := db.QueryRow(ctx, `
			SELECT id::text, email, username, status
			FROM testagram_identity.identity_applications
			WHERE email='first@example.test' AND status='approved'
		`).Scan(&applicationID, &applicationEmail, &username, &applicationStatus); err != nil {
			t.Fatalf("load approved identity fixture: %v", err)
		}
		applicationToken = strings.Repeat("a", 43)
		appwriteUserID = "appwrite-user-ci"
		appwriteEmail = "first@example.test"
		appwriteJWT = "identity-ci-jwt"
		appwriteProjectID = "identity-ci-project"
		emailVerified = false
		appwriteServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/account" {
				http.NotFound(w, r)
				return
			}
			if r.Header.Get("X-Appwrite-Project") != appwriteProjectID || r.Header.Get("X-Appwrite-JWT") != appwriteJWT {
				http.Error(w, "invalid test credentials", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"$id": appwriteUserID, "email": appwriteEmail,
				"emailVerification": emailVerified, "status": true,
			})
		}))
		defer appwriteServer.Close()
		t.Setenv("APPWRITE_ENDPOINT", appwriteServer.URL+"/v1")
		t.Setenv("APPWRITE_PROJECT_ID", appwriteProjectID)
		upstream = appwriteServer.Client()
	}

	if _, err := db.Exec(ctx, `
		UPDATE testagram_identity.identity_applications
		SET status_access_token_sha256=$2,status_access_token_expires_at=now()+interval '7 days'
		WHERE id=$1::uuid
	`, applicationID, authTokenHash(applicationToken)); err != nil {
		t.Fatal(err)
	}

	svc := &service{db: db, upstream: upstream}
	tokenRequest := httptest.NewRequest(http.MethodPost, "/api/identity/applications/"+applicationID+"/provisioning-token", nil)
	tokenRequest.SetPathValue("application_id", applicationID)
	tokenRequest.Header.Set("X-Application-Token", applicationToken)
	tokenResponse := httptest.NewRecorder()
	svc.identityProvisioningToken(tokenResponse, tokenRequest)
	if tokenResponse.Code != http.StatusOK {
		t.Fatalf("provisioning token status=%d body=%s", tokenResponse.Code, tokenResponse.Body.String())
	}
	var grant map[string]any
	if err := json.Unmarshal(tokenResponse.Body.Bytes(), &grant); err != nil {
		t.Fatal(err)
	}
	approvalToken, _ := grant["approval_token"].(string)
	jti, _ := grant["jti"].(string)
	if len(approvalToken) < 32 || len(jti) < 32 {
		t.Fatalf("short-lived approval grant was incomplete: %#v", grant)
	}

	redeem := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/identity/applications/"+applicationID+"/redeem", nil)
		req.SetPathValue("application_id", applicationID)
		req.Header.Set("X-Application-Token", applicationToken)
		req.Header.Set("X-Identity-Approval-Token", approvalToken)
		req.Header.Set("X-Identity-Approval-JTI", jti)
		req.Header.Set("Authorization", "Bearer "+appwriteJWT)
		recorder := httptest.NewRecorder()
		svc.identityProvisioningRedeem(recorder, req)
		return recorder
	}

	first := redeem()
	if first.Code != http.StatusOK {
		t.Fatalf("first redemption status=%d body=%s", first.Code, first.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(first.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	canonicalID, _ := result["canonical_user_id"].(string)
	if canonicalID == "" || result["email_verification_required"] != !emailVerified {
		t.Fatalf("unexpected provisioning result: %#v", result)
	}

	expectedEmailVerified := emailVerified
	var accountCount, mappingCount, completedCount int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM testagram_local.accounts WHERE user_id=$1::uuid AND username=$2", canonicalID, username).Scan(&accountCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "SELECT count(*) FROM testagram_identity.appwrite_accounts WHERE appwrite_user_id=$1 AND canonical_user_id=$2::uuid AND email_verified=$3", appwriteUserID, canonicalID, expectedEmailVerified).Scan(&mappingCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "SELECT count(*) FROM testagram_identity.registration_provisioning WHERE application_id=$1::uuid AND status='completed' AND appwrite_user_id=$2 AND canonical_user_id=$3::uuid", applicationID, appwriteUserID, canonicalID).Scan(&completedCount); err != nil {
		t.Fatal(err)
	}
	if accountCount != 1 || mappingCount != 1 || completedCount != 1 {
		t.Fatalf("canonical provisioning invariants: account=%d mapping=%d saga=%d", accountCount, mappingCount, completedCount)
	}

	// Email verification is confirmed by Appwrite at the authenticated API
	// boundary, then synchronized to the canonical identity application.
	if !liveAppwrite {
		emailVerified = true
	}
	authRequest := httptest.NewRequest(http.MethodGet, "/api/profile", nil)
	authenticatedCanonicalID, authStatus, authErr := svc.authenticatedAppwriteUser(authRequest, appwriteJWT)
	if authErr != nil || authStatus != http.StatusOK || authenticatedCanonicalID != canonicalUserIDFromResult(t, result) {
		t.Fatalf("verified Appwrite login canonical=%q status=%d err=%v", authenticatedCanonicalID, authStatus, authErr)
	}
	var applicationStatusAfterLogin string
	if err := db.QueryRow(ctx, "SELECT status FROM testagram_identity.identity_applications WHERE id=$1::uuid", applicationID).Scan(&applicationStatusAfterLogin); err != nil {
		t.Fatal(err)
	}
	if applicationStatusAfterLogin != "verified" {
		t.Fatalf("application status after verified Appwrite login=%q, want verified", applicationStatusAfterLogin)
	}

	replay := redeem()
	if replay.Code != http.StatusOK {
		t.Fatalf("idempotent redemption status=%d body=%s", replay.Code, replay.Body.String())
	}
	var replayResult map[string]any
	if err := json.Unmarshal(replay.Body.Bytes(), &replayResult); err != nil {
		t.Fatal(err)
	}
	if replayResult["idempotent_replay"] != true || replayResult["canonical_user_id"] != authenticatedCanonicalID {
		t.Fatalf("redemption retry did not return the same canonical account: %#v", replayResult)
	}

	// A valid Appwrite JWT and completed provisioning mapping must not revive
	// a canonical account that has subsequently been suspended or deactivated.
	if _, err := db.Exec(ctx, `
		UPDATE testagram_local.accounts SET account_status='suspended' WHERE user_id=$1::uuid
	`, authenticatedCanonicalID); err != nil {
		t.Fatal(err)
	}
	inactiveID, inactiveStatus, inactiveErr := svc.authenticatedAppwriteUser(
		httptest.NewRequest(http.MethodGet, "/api/me/profile", nil), appwriteJWT,
	)
	if inactiveStatus != http.StatusForbidden || inactiveErr == nil || inactiveID != "" {
		t.Fatalf("inactive canonical account canonical=%q status=%d err=%v; want empty identity and 403",
			inactiveID, inactiveStatus, inactiveErr)
	}
	if _, err := db.Exec(ctx, `
		UPDATE testagram_local.accounts SET account_status='active' WHERE user_id=$1::uuid
	`, authenticatedCanonicalID); err != nil {
		t.Fatal(err)
	}

	if !liveAppwrite {
		appwriteUserID = "different-appwrite-user"
		replay = redeem()
		if replay.Code != http.StatusConflict {
			t.Fatalf("different Appwrite identity redemption status=%d, want 409 body=%s", replay.Code, replay.Body.String())
		}
	}
}

func canonicalUserIDFromResult(t *testing.T, result map[string]any) string {
	t.Helper()
	value, _ := result["canonical_user_id"].(string)
	return value
}


func TestVerifiedAppwriteUserCannotBypassTwoPersonIdentityApproval(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL identity authorization integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Ping(ctx); err != nil {
		t.Fatal(err)
	}

	appwriteID := "unapproved-" + strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "")
	email := appwriteID + "@example.test"
	jwt := "unapproved-appwrite-session-jwt"
	project := "identity-gate-ci-project"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/account" ||
			r.Header.Get("X-Appwrite-Project") != project ||
			r.Header.Get("X-Appwrite-JWT") != jwt {
			http.Error(w, "invalid test Appwrite request", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"$id": appwriteID, "email": email, "emailVerification": true, "status": true,
		})
	}))
	defer server.Close()
	t.Setenv("APPWRITE_ENDPOINT", server.URL+"/v1")
	t.Setenv("APPWRITE_PROJECT_ID", project)

	svc := &service{db: db, upstream: server.Client()}
	canonicalID, status, err := svc.authenticatedAppwriteUser(
		httptest.NewRequest(http.MethodGet, "/api/me/profile", nil), jwt,
	)
	if status != http.StatusForbidden || err == nil ||
		!strings.Contains(err.Error(), "two-person account approval") {
		t.Fatalf("unapproved verified Appwrite account canonical=%q status=%d err=%v; want explicit approval denial",
			canonicalID, status, err)
	}

	var mappings, canonicalAccounts int
	if err := db.QueryRow(ctx, `
		SELECT count(*) FROM testagram_identity.appwrite_accounts WHERE appwrite_user_id=$1
	`, appwriteID).Scan(&mappings); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `
		SELECT count(*) FROM testagram_local.accounts WHERE username LIKE $1
	`, appwriteDefaultUsername(email, appwriteID)).Scan(&canonicalAccounts); err != nil {
		t.Fatal(err)
	}
	if mappings != 0 || canonicalAccounts != 0 {
		t.Fatalf("unapproved login created account state: mappings=%d canonical_accounts=%d", mappings, canonicalAccounts)
	}
}


// TestIdentityApprovalRejectsExpiredSensitiveActionRequest proves that the
// identity-specific approval handler cannot bypass the expiry enforced by the
// general sensitive-action approval path.
func TestIdentityApprovalRejectsExpiredSensitiveActionRequest(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL identity approval integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	const applicantEmail = "expired-approval@example.test"
	const reviewerOne = "ci-reviewer-one"
	const reviewerTwo = "ci-reviewer-two"
	const reviewerEmail = "reviewer-one@example.test"
	const project = "identity-expiry-ci-project"
	const jwt = "identity-expiry-ci-jwt"

	var applicationID, requestID string
	if err := db.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&applicationID); err != nil {
		t.Fatal(err)
	}
	// Use a fingerprint distinct from the shared migration harness fixtures.
	fingerprint := strings.Repeat("f", 64)
	payload := map[string]any{
		"application_id": applicationID,
		"email": applicantEmail,
		"username": "expired_ci",
		"display_name": "Expired Approval",
		"evidence_fingerprint_sha256": fingerprint,
		"terms_version": "terms-v1",
		"privacy_version": "privacy-v1",
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `
		INSERT INTO testagram_auth.sensitive_action_requests
		  (action_type,requested_by_appwrite_user_id,target_reference,reason,payload,payload_sha256,required_approvals,status,expires_at)
		VALUES ('identity.approve','ci-intake-service',$1,
		        'Expired identity approval integration fixture',$2::jsonb,repeat('a',64),2,'pending',now()-interval '1 minute')
		RETURNING id::text
	`, applicationID, string(payloadJSON)).Scan(&requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO testagram_identity.identity_applications
		  (id,email,username,display_name,adult_confirmed_at,adult_verified_at,
		   adult_verification_provider,adult_verification_reference,adult_verification_checked_at,
		   evidence_reference,evidence_fingerprint_sha256,terms_version,privacy_version,
		   terms_accepted_at,privacy_accepted_at,status,sensitive_action_request_id)
		VALUES ($1::uuid,$2,'expired_ci','Expired Approval',now(),now(),
		        'testagram_manual','expired-ci-adult-ref',now(),
		        'opaque-expired-evidence-ref',decode($3,'hex'),'terms-v1','privacy-v1',
		        now(),now(),'verified',$4::uuid)
	`, applicationID, applicantEmail, fingerprint, requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO testagram_identity.identity_application_reviews
		  (application_id,reviewer_appwrite_user_id,decision,reason,evidence_audit_reference)
		VALUES ($1::uuid,$2,'approved','First independent evidence review','expired-audit-ref-01'),
		       ($1::uuid,$3,'approved','Second independent evidence review','expired-audit-ref-02')
	`, applicationID, reviewerOne, reviewerTwo); err != nil {
		t.Fatal(err)
	}
	// Simulate the first approval already being recorded before the request
	// expires. The second reviewer must not be able to finalize it afterwards.
	if _, err := db.Exec(ctx, `
		INSERT INTO testagram_auth.sensitive_action_approvals
		  (request_id,approver_appwrite_user_id,decision,reason)
		VALUES ($1::uuid,$2,'approved','First approval before expiry')
	`, requestID, reviewerTwo); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/account" ||
			r.Header.Get("X-Appwrite-Project") != project ||
			r.Header.Get("X-Appwrite-JWT") != jwt {
			http.Error(w, "invalid test Appwrite request", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"$id": reviewerOne, "email": reviewerEmail,
			"emailVerification": true, "status": true,
		})
	}))
	defer server.Close()
	t.Setenv("APPWRITE_ENDPOINT", server.URL+"/v1")
	t.Setenv("APPWRITE_PROJECT_ID", project)

	svc := &service{db: db, upstream: server.Client()}
	req := httptest.NewRequest(http.MethodPost,
		"/api/admin/identity/applications/"+applicationID+"/approve",
		strings.NewReader(`{"reason":"Second reviewer approval after expiry"}`))
	req.SetPathValue("application_id", applicationID)
	req.Header.Set("Authorization", "Bearer "+jwt)
	response := httptest.NewRecorder()
	svc.identityApplicationApprove(response, req)
	if response.Code != http.StatusConflict ||
		!strings.Contains(response.Body.String(), "APPROVAL_REQUEST_EXPIRED_OR_INVALID") {
		t.Fatalf("expired approval status=%d body=%s; want explicit conflict", response.Code, response.Body.String())
	}

	var applicationStatus, requestStatus string
	var approvalCount, tokenCount int
	if err := db.QueryRow(ctx, "SELECT status FROM testagram_identity.identity_applications WHERE id=$1::uuid", applicationID).Scan(&applicationStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "SELECT status FROM testagram_auth.sensitive_action_requests WHERE id=$1::uuid", requestID).Scan(&requestStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "SELECT count(*) FROM testagram_auth.sensitive_action_approvals WHERE request_id=$1::uuid", requestID).Scan(&approvalCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "SELECT count(*) FROM testagram_identity.identity_approval_tokens WHERE application_id=$1::uuid", applicationID).Scan(&tokenCount); err != nil {
		t.Fatal(err)
	}
	if applicationStatus != "verified" || requestStatus != "pending" || approvalCount != 1 || tokenCount != 0 {
		t.Fatalf("expired approval mutated state: application=%q request=%q approvals=%d tokens=%d",
			applicationStatus, requestStatus, approvalCount, tokenCount)
	}
}
