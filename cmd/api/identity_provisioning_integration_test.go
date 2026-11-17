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

func nativeStaffReviewerFixture(t *testing.T, ctx context.Context, db *pgxpool.Pool) (string, string, string) {
	t.Helper()
	t.Setenv("TESTAGRAM_AUTH_JWT_SECRET", strings.Repeat("s", 64))
	passwordHash, err := authPasswordHash("native-reviewer-password-123")
	if err != nil {
		t.Fatal(err)
	}
	newIdentity := func(prefix string) (string, string) {
		t.Helper()
		var userID string
		if err := db.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&userID); err != nil {
			t.Fatal(err)
		}
		compact := strings.ReplaceAll(userID, "-", "")
		email := prefix + "-" + compact[:12] + "@example.test"
		username := prefix + "_" + compact[:12]
		if _, err := db.Exec(ctx, "INSERT INTO testagram_auth.identities(user_id,email,username,password_hash,email_verified_at,token_version) VALUES($1::uuid,$2,$3,$4,now(),1)", userID, email, username, passwordHash); err != nil {
			t.Fatal(err)
		}
		return userID, email
	}
	reviewerID, reviewerEmail := newIdentity("reviewer")
	requesterID, _ := newIdentity("requester")
	approverID, _ := newIdentity("approver")
	if _, err := db.Exec(ctx, "INSERT INTO testagram_auth.staff_principals(user_id,enabled,mfa_enrolled_at,enabled_at,enabled_by) VALUES($1::uuid,TRUE,now(),now(),$2::uuid)", reviewerID, approverID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, "INSERT INTO testagram_auth.staff_role_grants(user_id,role,granted_by) VALUES($1::uuid,'administrator',$2::uuid)", reviewerID, approverID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, "INSERT INTO testagram_auth.staff_approval_events(subject_user_id,action,role,requested_by,approved_by,requested_at,approved_at,applied_at) VALUES($1::uuid,'grant_role','administrator',$2::uuid,$3::uuid,now()-interval '1 minute',now(),now())", reviewerID, requesterID, approverID); err != nil {
		t.Fatal(err)
	}
	session, err := issueNativeSession(ctx, db, reviewerID, reviewerEmail, 1, "ci-native-reviewer", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	return reviewerID, reviewerEmail, session.AccessToken
}

func TestNativeIdentityProvisioningTokenAndRedeemIntegration(t *testing.T) {
	t.Skip("Legacy Appwrite provisioning is superseded by the Testagram-native approved identity, email verification and session integration.")
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
			       (SELECT count(DISTINCT r.reviewer_user_id) FROM testagram_identity.identity_application_reviews r
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
			t.Fatal("staging Appwrite identity does not match the approved application; email values are intentionally redacted")
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

	// Race two requests with the same one-time authorization. Exactly one
	// transaction may create the canonical mapping; the concurrent retry must
	// resolve to that same account rather than create a duplicate.
	redemptions := make(chan *httptest.ResponseRecorder, 2)
	go func() { redemptions <- redeem() }()
	go func() { redemptions <- redeem() }()
	first, second := <-redemptions, <-redemptions
	for _, response := range []*httptest.ResponseRecorder{first, second} {
		if response.Code != http.StatusOK {
			t.Fatalf("concurrent redemption status=%d body=%s", response.Code, response.Body.String())
		}
	}
	var result, concurrentResult map[string]any
	if err := json.Unmarshal(first.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(second.Body.Bytes(), &concurrentResult); err != nil {
		t.Fatal(err)
	}
	canonicalID, _ := result["canonical_user_id"].(string)
	concurrentCanonicalID, _ := concurrentResult["canonical_user_id"].(string)
	if canonicalID == "" || concurrentCanonicalID != canonicalID ||
		result["email_verification_required"] != !emailVerified ||
		concurrentResult["email_verification_required"] != !emailVerified {
		t.Fatalf("concurrent redemption did not resolve one canonical account: first=%#v second=%#v", result, concurrentResult)
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
	// Restore the staging fixture even if an assertion below fails, so a
	// failed smoke test cannot leave its canonical account suspended.
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = db.Exec(cleanupCtx, `
			UPDATE testagram_local.accounts SET account_status='active' WHERE user_id=$1::uuid
		`, authenticatedCanonicalID)
	}()
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

func TestExternalProviderTokenCannotCreateOrAuthorizeTestagramAccount(t *testing.T) {
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
	t.Setenv("TESTAGRAM_AUTH_JWT_SECRET", strings.Repeat("n", 64))
	legacyID := "external-" + strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "")
	email := legacyID + "@example.test"
	legacyEndpointCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		legacyEndpointCalled = true
		http.Error(w, "legacy provider must not be called", http.StatusUnauthorized)
	}))
	defer server.Close()
	t.Setenv("APPWRITE_ENDPOINT", server.URL+"/v1")
	t.Setenv("APPWRITE_PROJECT_ID", "ci-only-project")
	svc := &service{db: db, upstream: server.Client()}
	canonicalID, status, err := svc.authenticatedAppwriteUser(httptest.NewRequest(http.MethodGet, "/api/me/profile", nil), "external-provider-session-token")
	if status != http.StatusUnauthorized || err == nil || canonicalID != "" {
		t.Fatalf("external provider token canonical=%q status=%d err=%v; native Testagram auth must reject it", canonicalID, status, err)
	}
	if legacyEndpointCalled {
		t.Fatal("native authentication attempted an Appwrite account lookup")
	}
	var mappings, canonicalAccounts int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM testagram_identity.appwrite_accounts WHERE appwrite_user_id=$1", legacyID).Scan(&mappings); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "SELECT count(*) FROM testagram_local.accounts WHERE username=$1", appwriteDefaultUsername(email, legacyID)).Scan(&canonicalAccounts); err != nil {
		t.Fatal(err)
	}
	if mappings != 0 || canonicalAccounts != 0 {
		t.Fatalf("external provider attempt created Testagram account state: mappings=%d canonical_accounts=%d", mappings, canonicalAccounts)
	}
}
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
	reviewerOneID, _, reviewerOneToken := nativeStaffReviewerFixture(t, ctx, db)
	reviewerTwoID, _, _ := nativeStaffReviewerFixture(t, ctx, db)
	const applicantEmail = "expired-approval@example.test"
	var applicationID, requestID string
	if err := db.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&applicationID); err != nil {
		t.Fatal(err)
	}
	fingerprint := strings.Repeat("f", 64)
	payload, err := json.Marshal(map[string]any{"application_id": applicationID, "email": applicantEmail, "username": "expired_ci", "display_name": "Expired Approval", "evidence_fingerprint_sha256": fingerprint, "terms_version": "terms-v1", "privacy_version": "privacy-v1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "INSERT INTO testagram_auth.sensitive_action_requests(action_type,requested_by_appwrite_user_id,target_reference,reason,payload,payload_sha256,required_approvals,status,expires_at) VALUES('identity.approve','ci-intake-service',$1,'Expired identity approval integration fixture',$2::jsonb,repeat('a',64),2,'pending',now()-interval '1 minute') RETURNING id::text", applicationID, string(payload)).Scan(&requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, "INSERT INTO testagram_identity.identity_applications(id,email,username,display_name,adult_confirmed_at,adult_verified_at,adult_verification_provider,adult_verification_reference,adult_verification_checked_at,evidence_reference,evidence_fingerprint_sha256,terms_version,privacy_version,terms_accepted_at,privacy_accepted_at,status,sensitive_action_request_id) VALUES($1::uuid,$2,'expired_ci','Expired Approval',now(),now(),'testagram_manual','expired-ci-adult-ref',now(),'opaque-expired-evidence-ref',decode($3,'hex'),'terms-v1','privacy-v1',now(),now(),'verified',$4::uuid)", applicationID, applicantEmail, fingerprint, requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, "INSERT INTO testagram_identity.identity_application_reviews(application_id,reviewer_user_id,decision,reason,evidence_audit_reference) VALUES($1::uuid,$2,'approved','First independent evidence review','expired-audit-ref-01'),($1::uuid,$3,'approved','Second independent evidence review','expired-audit-ref-02')", applicationID, reviewerOneID, reviewerTwoID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, "INSERT INTO testagram_auth.sensitive_action_approvals(request_id,approver_appwrite_user_id,decision,reason) VALUES($1::uuid,$2,'approved','First approval before expiry')", requestID, reviewerTwoID); err != nil {
		t.Fatal(err)
	}
	svc := &service{db: db}
	req := httptest.NewRequest(http.MethodPost, "/api/admin/identity/applications/"+applicationID+"/approve", strings.NewReader("{\"reason\":\"Second reviewer approval after expiry\"}"))
	req.SetPathValue("application_id", applicationID)
	req.Header.Set("Authorization", "Bearer "+reviewerOneToken)
	response := httptest.NewRecorder()
	svc.identityApplicationApprove(response, req)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "APPROVAL_REQUEST_EXPIRED_OR_INVALID") {
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
		t.Fatalf("expired approval mutated state: application=%q request=%q approvals=%d tokens=%d", applicationStatus, requestStatus, approvalCount, tokenCount)
	}
}
func TestIdentityReviewerHonorsNativeRoleRevocationAndMFA(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL reviewer authorization integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reviewerID, reviewerEmail, token := nativeStaffReviewerFixture(t, ctx, db)
	svc := &service{db: db}
	authorize := func() (int, error) {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/identity/review-queue", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		reviewer, status, err := svc.identityReviewer(req)
		if err == nil && (reviewer.ID != reviewerID || reviewer.Email != reviewerEmail) {
			t.Fatalf("native reviewer identity=%q email=%q; want %q / %q", reviewer.ID, reviewer.Email, reviewerID, reviewerEmail)
		}
		return status, err
	}
	if status, err := authorize(); status != http.StatusOK || err != nil {
		t.Fatalf("active native administrator reviewer status=%d err=%v; want 200", status, err)
	}
	if _, err := db.Exec(ctx, "UPDATE testagram_auth.staff_role_grants SET revoked_at=now(),revoked_by=granted_by WHERE user_id=$1::uuid AND role='administrator' AND revoked_at IS NULL", reviewerID); err != nil {
		t.Fatal(err)
	}
	if status, err := authorize(); status != http.StatusForbidden || err == nil {
		t.Fatalf("revoked native role status=%d err=%v; want 403", status, err)
	}
	if _, err := db.Exec(ctx, "UPDATE testagram_auth.staff_role_grants SET revoked_at=NULL,revoked_by=NULL WHERE user_id=$1::uuid AND role='administrator'", reviewerID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, "UPDATE testagram_auth.staff_role_grants SET role='support' WHERE user_id=$1::uuid AND role='administrator' AND revoked_at IS NULL", reviewerID); err != nil {
		t.Fatal(err)
	}
	if status, err := authorize(); status != http.StatusForbidden || err == nil {
		t.Fatalf("unsupported native staff role status=%d err=%v; want 403", status, err)
	}
	if _, err := db.Exec(ctx, "UPDATE testagram_auth.staff_role_grants SET role='administrator' WHERE user_id=$1::uuid AND role='support' AND revoked_at IS NULL", reviewerID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, "UPDATE testagram_auth.staff_principals SET enabled=FALSE WHERE user_id=$1::uuid", reviewerID); err != nil {
		t.Fatal(err)
	}
	if status, err := authorize(); status != http.StatusForbidden || err == nil {
		t.Fatalf("disabled native staff principal status=%d err=%v; want 403", status, err)
	}
	if _, err := db.Exec(ctx, "UPDATE testagram_auth.staff_principals SET enabled=TRUE,mfa_enrolled_at=NULL WHERE user_id=$1::uuid", reviewerID); err != nil {
		t.Fatal(err)
	}
	if status, err := authorize(); status != http.StatusForbidden || err == nil {
		t.Fatalf("native reviewer without enrolled MFA status=%d err=%v; want 403", status, err)
	}
}
func TestIdentityApprovalRejectsReviewerOutsideApplicationReviewPair(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL reviewer separation integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reviewerID, _, reviewerToken := nativeStaffReviewerFixture(t, ctx, db)
	var reviewerOneID, reviewerTwoID string
	if err := db.QueryRow(ctx, "SELECT gen_random_uuid()::text,gen_random_uuid()::text").Scan(&reviewerOneID, &reviewerTwoID); err != nil {
		t.Fatal(err)
	}
	const applicantEmail = "reviewer-mismatch@example.test"
	var applicationID, requestID string
	if err := db.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&applicationID); err != nil {
		t.Fatal(err)
	}
	fingerprint := strings.Repeat("b", 64)
	payload, err := json.Marshal(map[string]any{"application_id": applicationID, "email": applicantEmail, "username": "review_mismatch", "display_name": "Reviewer Mismatch", "evidence_fingerprint_sha256": fingerprint, "terms_version": "terms-v1", "privacy_version": "privacy-v1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "INSERT INTO testagram_auth.sensitive_action_requests(action_type,requested_by_appwrite_user_id,target_reference,reason,payload,payload_sha256,required_approvals,status,expires_at) VALUES('identity.approve','ci-intake-service',$1,'Reviewer separation integration fixture',$2::jsonb,repeat('c',64),2,'pending',now()+interval '1 hour') RETURNING id::text", applicationID, string(payload)).Scan(&requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, "INSERT INTO testagram_identity.identity_applications(id,email,username,display_name,adult_confirmed_at,adult_verified_at,adult_verification_provider,adult_verification_reference,adult_verification_checked_at,evidence_reference,evidence_fingerprint_sha256,terms_version,privacy_version,terms_accepted_at,privacy_accepted_at,status,sensitive_action_request_id) VALUES($1::uuid,$2,'review_mismatch','Reviewer Mismatch',now(),now(),'testagram_manual','review-mismatch-adult-ref',now(),'opaque-review-mismatch-evidence',decode($3,'hex'),'terms-v1','privacy-v1',now(),now(),'verified',$4::uuid)", applicationID, applicantEmail, fingerprint, requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, "INSERT INTO testagram_identity.identity_application_reviews(application_id,reviewer_user_id,decision,reason,evidence_audit_reference) VALUES($1::uuid,$2,'approved','First independent evidence review','mismatch-audit-01'),($1::uuid,$3,'approved','Second independent evidence review','mismatch-audit-02')", applicationID, reviewerOneID, reviewerTwoID); err != nil {
		t.Fatal(err)
	}
	svc := &service{db: db}
	req := httptest.NewRequest(http.MethodPost, "/api/admin/identity/applications/"+applicationID+"/approve", strings.NewReader("{\"reason\":\"Attempt by a non-reviewer to approve this application\"}"))
	req.SetPathValue("application_id", applicationID)
	req.Header.Set("Authorization", "Bearer "+reviewerToken)
	response := httptest.NewRecorder()
	svc.identityApplicationApprove(response, req)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "APPROVAL_REVIEWER_MISMATCH") {
		t.Fatalf("non-reviewer approval status=%d body=%s; want 403 reviewer mismatch (native reviewer %s)", response.Code, response.Body.String(), reviewerID)
	}
	var approvalCount, tokenCount int
	var applicationStatus string
	if err := db.QueryRow(ctx, "SELECT count(*) FROM testagram_auth.sensitive_action_approvals WHERE request_id=$1::uuid", requestID).Scan(&approvalCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "SELECT count(*) FROM testagram_identity.identity_approval_tokens WHERE application_id=$1::uuid", applicationID).Scan(&tokenCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "SELECT status FROM testagram_identity.identity_applications WHERE id=$1::uuid", applicationID).Scan(&applicationStatus); err != nil {
		t.Fatal(err)
	}
	if approvalCount != 0 || tokenCount != 0 || applicationStatus != "verified" {
		t.Fatalf("non-reviewer changed approval state: approvals=%d tokens=%d application=%q", approvalCount, tokenCount, applicationStatus)
	}
}
func TestIdentityApprovalSerializesConcurrentReviewersAndDuplicateApprovals(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL identity concurrency integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reviewerOneID, _, reviewerOneToken := nativeStaffReviewerFixture(t, ctx, db)
	reviewerTwoID, _, reviewerTwoToken := nativeStaffReviewerFixture(t, ctx, db)
	svc := &service{db: db}

	createFixture := func(email, username, fingerprint string) (string, string) {
		t.Helper()
		var applicationID, requestID string
		if err := db.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&applicationID); err != nil {
			t.Fatal(err)
		}
		payload, err := json.Marshal(map[string]any{"application_id": applicationID, "email": email, "username": username, "display_name": "Concurrent Review", "evidence_fingerprint_sha256": fingerprint, "terms_version": "terms-v1", "privacy_version": "privacy-v1"})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(ctx, "INSERT INTO testagram_auth.sensitive_action_requests(action_type,requested_by_appwrite_user_id,target_reference,reason,payload,payload_sha256,required_approvals,status,expires_at) VALUES('identity.approve','ci-intake-service',$1,'Concurrent identity approval integration fixture',$2::jsonb,repeat('a',64),2,'pending',now()+interval '1 hour') RETURNING id::text", applicationID, string(payload)).Scan(&requestID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, "INSERT INTO testagram_identity.identity_applications(id,email,username,display_name,adult_confirmed_at,adult_verified_at,adult_verification_provider,adult_verification_reference,adult_verification_checked_at,evidence_reference,evidence_fingerprint_sha256,terms_version,privacy_version,terms_accepted_at,privacy_accepted_at,status,sensitive_action_request_id) VALUES($1::uuid,$2,$3,'Concurrent Review',now(),now(),'testagram_manual','concurrent-adult-ref',now(),'opaque-concurrent-evidence-ref',decode($4,'hex'),'terms-v1','privacy-v1',now(),now(),'verified',$5::uuid)", applicationID, email, username, fingerprint, requestID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, "INSERT INTO testagram_identity.identity_application_reviews(application_id,reviewer_user_id,decision,reason,evidence_audit_reference) VALUES($1::uuid,$2,'approved','First independent evidence review','concurrent-audit-01'),($1::uuid,$3,'approved','Second independent evidence review','concurrent-audit-02')", applicationID, reviewerOneID, reviewerTwoID); err != nil {
			t.Fatal(err)
		}
		return applicationID, requestID
	}
	approve := func(applicationID, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/admin/identity/applications/"+applicationID+"/approve", strings.NewReader("{\"reason\":\"Concurrent independent approval decision\"}"))
		req.SetPathValue("application_id", applicationID)
		req.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		svc.identityApplicationApprove(response, req)
		return response
	}

	applicationID, requestID := createFixture("concurrent-review@example.test", "concurrent_review", strings.Repeat("e", 64))
	results := make(chan *httptest.ResponseRecorder, 2)
	go func() { results <- approve(applicationID, reviewerOneToken) }()
	go func() { results <- approve(applicationID, reviewerTwoToken) }()
	first, second := <-results, <-results
	statuses := map[int]int{first.Code: 1}
	statuses[second.Code]++
	if statuses[http.StatusAccepted] != 1 || statuses[http.StatusOK] != 1 {
		t.Fatalf("concurrent distinct reviewers returned statuses %d and %d; want one 202 and one 200: %s | %s", first.Code, second.Code, first.Body.String(), second.Body.String())
	}
	var approvals, tokens int
	var applicationStatus, requestStatus string
	if err := db.QueryRow(ctx, "SELECT count(*) FROM testagram_auth.sensitive_action_approvals WHERE request_id=$1::uuid AND decision='approved'", requestID).Scan(&approvals); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "SELECT count(*) FROM testagram_identity.identity_approval_tokens WHERE application_id=$1::uuid", applicationID).Scan(&tokens); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "SELECT status FROM testagram_identity.identity_applications WHERE id=$1::uuid", applicationID).Scan(&applicationStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "SELECT status FROM testagram_auth.sensitive_action_requests WHERE id=$1::uuid", requestID).Scan(&requestStatus); err != nil {
		t.Fatal(err)
	}
	if approvals != 2 || tokens != 1 || applicationStatus != "approved" || requestStatus != "approved" {
		t.Fatalf("distinct-reviewer race state: approvals=%d tokens=%d application=%q request=%q", approvals, tokens, applicationStatus, requestStatus)
	}

	duplicateApplicationID, duplicateRequestID := createFixture("duplicate-review@example.test", "duplicate_review", strings.Repeat("0", 64))
	duplicateResults := make(chan *httptest.ResponseRecorder, 2)
	go func() { duplicateResults <- approve(duplicateApplicationID, reviewerOneToken) }()
	go func() { duplicateResults <- approve(duplicateApplicationID, reviewerOneToken) }()
	duplicateFirst, duplicateSecond := <-duplicateResults, <-duplicateResults
	duplicateStatuses := map[int]int{duplicateFirst.Code: 1}
	duplicateStatuses[duplicateSecond.Code]++
	if duplicateStatuses[http.StatusAccepted] != 1 || duplicateStatuses[http.StatusConflict] != 1 {
		t.Fatalf("duplicate reviewer race returned statuses %d and %d; want one 202 and one 409: %s | %s", duplicateFirst.Code, duplicateSecond.Code, duplicateFirst.Body.String(), duplicateSecond.Body.String())
	}
	if err := db.QueryRow(ctx, "SELECT count(*) FROM testagram_auth.sensitive_action_approvals WHERE request_id=$1::uuid", duplicateRequestID).Scan(&approvals); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "SELECT count(*) FROM testagram_identity.identity_approval_tokens WHERE application_id=$1::uuid", duplicateApplicationID).Scan(&tokens); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "SELECT status FROM testagram_identity.identity_applications WHERE id=$1::uuid", duplicateApplicationID).Scan(&applicationStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "SELECT status FROM testagram_auth.sensitive_action_requests WHERE id=$1::uuid", duplicateRequestID).Scan(&requestStatus); err != nil {
		t.Fatal(err)
	}
	if approvals != 1 || tokens != 0 || applicationStatus != "verified" || requestStatus != "pending" {
		t.Fatalf("duplicate-reviewer race state: approvals=%d tokens=%d application=%q request=%q", approvals, tokens, applicationStatus, requestStatus)
	}
}
