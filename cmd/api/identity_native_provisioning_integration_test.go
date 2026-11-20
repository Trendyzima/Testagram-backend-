package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNativeApprovedIdentityProvisioningAndEmailVerification(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for native approved-identity provisioning integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	suffix := fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	var reviewerOne, reviewerTwo, approverOne, approverTwo, staffGrantor string
	if err := db.QueryRow(ctx, "SELECT gen_random_uuid()::text,gen_random_uuid()::text,gen_random_uuid()::text,gen_random_uuid()::text,gen_random_uuid()::text").
		Scan(&reviewerOne, &reviewerTwo, &approverOne, &approverTwo, &staffGrantor); err != nil {
		t.Fatal(err)
	}
	applicantEmail := "native-" + suffix + "@example.test"
	username := "n" + suffix[len(suffix)-17:]
	applicationToken, err := authRandomToken()
	if err != nil {
		t.Fatal(err)
	}
	approvalToken, err := authRandomToken()
	if err != nil {
		t.Fatal(err)
	}
	jti, err := authRandomToken()
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := fmt.Sprintf("%064x", time.Now().UTC().UnixNano())

	for i, reviewer := range []string{reviewerOne, reviewerTwo, approverOne, approverTwo} {
		email := fmt.Sprintf("native-reviewer-%d-%s@example.test", i+1, suffix)
		username := fmt.Sprintf("nr%d_%s", i+1, suffix[len(suffix)-12:])
		if _, err := db.Exec(ctx, `
			INSERT INTO testagram_auth.identities(user_id,email,username,password_hash,email_verified_at)
			VALUES($1::uuid,$2,$3,'ci-only-invalid-password-hash',now())
		`, reviewer, email, username); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO testagram_auth.identities(user_id,email,username,password_hash,email_verified_at)
		VALUES($1::uuid,$2,$3,'ci-only-invalid-password-hash',now())
	`, staffGrantor, "native-staff-grantor-"+suffix+"@example.test", "nsg_"+suffix[len(suffix)-12:]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO testagram_auth.staff_principals(user_id,enabled,mfa_enrolled_at,enabled_at,enabled_by)
		VALUES
		  ($1::uuid,TRUE,now(),now(),$5::uuid),
		  ($2::uuid,TRUE,now(),now(),$5::uuid),
		  ($3::uuid,TRUE,now(),now(),$5::uuid),
		  ($4::uuid,TRUE,now(),now(),$5::uuid)
	`, reviewerOne, reviewerTwo, approverOne, approverTwo, staffGrantor); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO testagram_auth.staff_role_grants(user_id,role,granted_by)
		VALUES
		  ($1::uuid,'identity_reviewer',$5::uuid),
		  ($2::uuid,'identity_reviewer',$5::uuid),
		  ($3::uuid,'administrator',$5::uuid),
		  ($4::uuid,'administrator',$5::uuid)
	`, reviewerOne, reviewerTwo, approverOne, approverTwo, staffGrantor); err != nil {
		t.Fatal(err)
	}

	var applicationID, requestID, sessionID string
	if err := db.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&applicationID); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{
		"application_id": applicationID, "email": applicantEmail, "username": username,
		"display_name": "Native Provisioning CI", "evidence_fingerprint_sha256": fingerprint,
		"terms_version": "terms-v1", "privacy_version": "privacy-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `
		INSERT INTO testagram_auth.native_sensitive_action_requests
		  (action_type,requested_by_user_id,requested_by_service,target_reference,reason,payload,payload_sha256,
		   required_approvals,status,expires_at)
		VALUES('identity.approve',NULL,'identity_pipeline',$1,'Native provisioning integration fixture',
		       $2::jsonb,repeat('e',64),2,'pending',now()+interval '1 hour')
		RETURNING id::text
	`, applicationID, string(payload)).Scan(&requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO testagram_identity.identity_applications
		  (id,email,username,display_name,adult_confirmed_at,adult_verified_at,
		   adult_verification_provider,adult_verification_reference,adult_verification_checked_at,
		   evidence_reference,evidence_fingerprint_sha256,terms_version,privacy_version,
		   terms_accepted_at,privacy_accepted_at,submission_key_sha256,
		   status_access_token_sha256,status_access_token_expires_at,status,native_sensitive_action_request_id)
		VALUES($1::uuid,$2,$3,'Native Provisioning CI',now(),now(),
		       'testagram_manual','native-provisioning-adult-ref',now(),
		       'opaque-native-provisioning-evidence',decode($4,'hex'),'terms-v1','privacy-v1',
		       now(),now(),decode(repeat('f',64),'hex'),$5,now()+interval '7 days','verified',$6::uuid)
	`, applicationID, applicantEmail, username, fingerprint, authTokenHash(applicationToken), requestID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	sessionTokenHash := authTokenHash("native-verification-session-" + suffix)
	if _, err := db.Exec(ctx, `
		INSERT INTO testagram_identity.verification_sessions
		  (id,application_id,session_token_sha256,purpose,requested_minimum_age,status,
		   evidence_reference,evidence_fingerprint_sha256,assurance_method,assurance_level,
		   verified_age_over_threshold,expires_at,submitted_at,decided_at,decided_by_user_id)
		VALUES($1::uuid,$2::uuid,$5,'age_assurance',18,'verified',
		       'opaque-native-provisioning-evidence',decode($3,'hex'),'document_review','reviewed',
		       TRUE,now()+interval '1 day',now(),now(),$4::uuid)
	`, sessionID, applicationID, fingerprint, reviewerOne, sessionTokenHash); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO testagram_identity.identity_application_reviews
		  (application_id,reviewer_user_id,review_stage,decision,reason,evidence_audit_reference)
		VALUES($1::uuid,$2::uuid,'evidence_review','approved','First independent evidence review','native-evidence-audit-01'),
		      ($1::uuid,$3::uuid,'evidence_review','approved','Second independent evidence review','native-evidence-audit-02'),
		      ($1::uuid,$4::uuid,'account_approval','approved','First independent account approval','native-account-audit-01'),
		      ($1::uuid,$5::uuid,'account_approval','approved','Second independent account approval','native-account-audit-02')
	`, applicationID, reviewerOne, reviewerTwo, approverOne, approverTwo); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO testagram_auth.native_sensitive_action_approvals(request_id,approver_user_id,decision,reason)
		VALUES($1::uuid,$2::uuid,'approved','First independent account approval')
	`, requestID, approverOne); err != nil {
		t.Fatal(err)
	}
	// The approval request can reach its decision state, but the database must
	// refuse to mint a provisioning capability until both independent account
	// approvals exist. The trigger protects the capability issuance boundary.
	if _, err := db.Exec(ctx, `
		UPDATE testagram_auth.native_sensitive_action_requests SET status='approved',decided_at=now()
		WHERE id=$1::uuid
	`, requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		UPDATE testagram_identity.identity_applications SET status='approved'
		WHERE id=$1::uuid
	`, applicationID); err != nil {
		t.Fatal(err)
	}

	// Exercise the real provisioning-token HTTP endpoint with only one
	// account approver. It must not mint or return a capability.
	singleApprovalRouter := (&service{db: db}).routes()
	singleApprovalReq := httptest.NewRequest(http.MethodPost,
		"/api/identity/applications/"+applicationID+"/provisioning-token", nil)
	singleApprovalReq.Header.Set("X-Application-Token", applicationToken)
	singleApprovalResp := httptest.NewRecorder()
	singleApprovalRouter.ServeHTTP(singleApprovalResp, singleApprovalReq)
	if singleApprovalResp.Code != http.StatusConflict ||
		strings.Contains(singleApprovalResp.Body.String(), "approval_token") {
		t.Fatalf("provisioning token issued before second independent approval: status=%d body=%s",
			singleApprovalResp.Code, singleApprovalResp.Body.String())
	}
	approvalTx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := approvalTx.Exec(ctx, "SAVEPOINT single_approval_attempt"); err != nil {
		_ = approvalTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := approvalTx.Exec(ctx, `
		INSERT INTO testagram_identity.identity_approval_tokens
		  (token_sha256,jti_sha256,application_id,first_approver_user_id,
		   second_approver_user_id,expires_at)
		VALUES($1,$2,$3::uuid,$4::uuid,$5::uuid,now()+interval '15 minutes')
	`, authTokenHash(approvalToken), authTokenHash(jti), applicationID, approverOne, approverTwo); err == nil {
		_ = approvalTx.Rollback(ctx)
		t.Fatal("database issued provisioning capability with only one account approver")
	}
	if _, err := approvalTx.Exec(ctx, "ROLLBACK TO SAVEPOINT single_approval_attempt"); err != nil {
		_ = approvalTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := approvalTx.Exec(ctx, `
		INSERT INTO testagram_auth.native_sensitive_action_approvals(request_id,approver_user_id,decision,reason)
		VALUES($1::uuid,$2::uuid,'approved','Second independent account approval')
	`, requestID, approverTwo); err != nil {
		_ = approvalTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := approvalTx.Exec(ctx, `
		INSERT INTO testagram_identity.identity_approval_tokens
		  (token_sha256,jti_sha256,application_id,first_approver_user_id,
		   second_approver_user_id,expires_at)
		VALUES($1,$2,$3::uuid,$4::uuid,$5::uuid,now()+interval '15 minutes')
	`, authTokenHash(approvalToken), authTokenHash(jti), applicationID, approverOne, approverTwo); err != nil {
		_ = approvalTx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := approvalTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	t.Setenv("AUTH_PUBLIC_BASE_URL", "https://testagram.example")
	type deliveredEmail struct {
		recipient string
		subject   string
		html      string
	}
	mailDeliveries := make(chan deliveredEmail, 4)
	svc := &service{
		db: db,
		authEmailSender: func(_ context.Context, recipient, subject, html string) error {
			mailDeliveries <- deliveredEmail{recipient: recipient, subject: subject, html: html}
			return nil
		},
	}
	router := svc.routes()

	tokenReq := httptest.NewRequest(http.MethodPost, "/api/identity/applications/"+applicationID+"/provisioning-token", nil)
	tokenReq.Header.Set("X-Application-Token", applicationToken)
	tokenResp := httptest.NewRecorder()
	router.ServeHTTP(tokenResp, tokenReq)
	if tokenResp.Code != http.StatusOK {
		t.Fatalf("native grant issue status=%d body=%s", tokenResp.Code, tokenResp.Body.String())
	}
	var grant map[string]any
	if err := json.Unmarshal(tokenResp.Body.Bytes(), &grant); err != nil {
		t.Fatal(err)
	}
	issuedApproval, _ := grant["approval_token"].(string)
	issuedJTI, _ := grant["jti"].(string)
	if len(issuedApproval) < 32 || len(issuedJTI) < 32 {
		t.Fatalf("incomplete provisioning grant: %#v", grant)
	}

	redeem := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/identity/applications/"+applicationID+"/redeem",
			strings.NewReader("{\"password\":\"native-ci-password-123\"}"))
		req.Header.Set("X-Application-Token", applicationToken)
		req.Header.Set("X-Identity-Approval-Token", issuedApproval)
		req.Header.Set("X-Identity-Approval-JTI", issuedJTI)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	results := make(chan *httptest.ResponseRecorder, 2)
	go func() { results <- redeem() }()
	go func() { results <- redeem() }()
	first, second := <-results, <-results
	var userID string
	for _, response := range []*httptest.ResponseRecorder{first, second} {
		if response.Code != http.StatusAccepted && response.Code != http.StatusOK {
			t.Fatalf("concurrent native redemption status=%d body=%s", response.Code, response.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		got, _ := body["canonical_user_id"].(string)
		if got == "" {
			t.Fatalf("redemption omitted canonical user ID: %#v", body)
		}
		if userID != "" && userID != got {
			t.Fatalf("concurrent redemption created different users: %q != %q", userID, got)
		}
		userID = got
	}

	initialEmail := <-mailDeliveries
	if initialEmail.recipient != applicantEmail || initialEmail.subject != "Verify your Testagram account" {
		t.Fatalf("initial verification email recipient/subject mismatch: %#v", initialEmail)
	}
	tokenPattern := regexp.MustCompile(`[?&]token=([A-Za-z0-9_-]{32,128})`)
	initialTokenMatch := tokenPattern.FindStringSubmatch(initialEmail.html)
	if len(initialTokenMatch) != 2 || len(mailDeliveries) != 0 {
		t.Fatalf("expected exactly one initial verification email with a token, html=%q pending=%d", initialEmail.html, len(mailDeliveries))
	}
	initialVerificationToken := initialTokenMatch[1]

	var identityCount, profileCount, pendingCount, consumedCount int
	var passwordHash string
	if err := db.QueryRow(ctx, `
		SELECT count(*),min(password_hash)
		FROM testagram_auth.identities WHERE user_id=$1::uuid
	`, userID).Scan(&identityCount, &passwordHash); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "SELECT count(*) FROM testagram_local.accounts WHERE user_id=$1::uuid", userID).Scan(&profileCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `
		SELECT count(*) FROM testagram_identity.native_registration_provisioning
		WHERE application_id=$1::uuid AND user_id=$2::uuid AND status='verification_pending'
	`, applicationID, userID).Scan(&pendingCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `
		SELECT count(*) FROM testagram_identity.identity_approval_tokens
		WHERE application_id=$1::uuid AND consumed_at IS NOT NULL
		  AND consumed_by_native_user_id=$2::uuid
	`, applicationID, userID).Scan(&consumedCount); err != nil {
		t.Fatal(err)
	}
	if identityCount != 1 || profileCount != 1 || pendingCount != 1 || consumedCount != 1 ||
		!strings.HasPrefix(passwordHash, "argon2id$v=19$") {
		t.Fatalf("native provisioning invariants identity=%d profile=%d pending=%d consumed=%d hash_prefix=%q",
			identityCount, profileCount, pendingCount, consumedCount, strings.Split(passwordHash, "$")[0])
	}

	// A provisioned-but-unverified identity must not be able to obtain a session.
	t.Setenv("TESTAGRAM_AUTH_JWT_SECRET", strings.Repeat("s", 48))
	var nativeEmail string
	var tokenVersion int64
	if err := db.QueryRow(ctx, `
		SELECT email,token_version FROM testagram_auth.identities WHERE user_id=$1::uuid
	`, userID).Scan(&nativeEmail, &tokenVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := issueNativeSession(ctx, db, userID, nativeEmail, tokenVersion, "native-ci", "127.0.0.1"); err == nil {
		t.Fatal("native session was issued before email verification activated the approved provisioning record")
	}

	t.Setenv("TESTAGRAM_AUTH_PROVIDER", "native")
	pendingLogin := httptest.NewRequest(http.MethodPost, "/api/auth/login",
		strings.NewReader("{\"email\":\""+nativeEmail+"\",\"password\":\"native-ci-password-123\"}"))
	pendingLoginResp := httptest.NewRecorder()
	if !svc.authRespond(pendingLoginResp, pendingLogin, "login") ||
		pendingLoginResp.Code != http.StatusForbidden ||
		!strings.Contains(pendingLoginResp.Body.String(), "EMAIL_VERIFICATION_REQUIRED") {
		t.Fatalf("unverified approved identity login status=%d body=%s", pendingLoginResp.Code, pendingLoginResp.Body.String())
	}

	var previousVerificationHash []byte
	if err := db.QueryRow(ctx, `
		SELECT verification_token_hash FROM testagram_auth.identities WHERE user_id=$1::uuid
	`, userID).Scan(&previousVerificationHash); err != nil {
		t.Fatal(err)
	}
	resendReq := httptest.NewRequest(http.MethodPost,
		"/api/identity/applications/"+applicationID+"/resend-verification", nil)
	resendReq.Header.Set("X-Application-Token", applicationToken)
	resendResp := httptest.NewRecorder()
	router.ServeHTTP(resendResp, resendReq)
	if resendResp.Code != http.StatusAccepted {
		t.Fatalf("native verification resend status=%d body=%s", resendResp.Code, resendResp.Body.String())
	}
	var rotatedVerificationHash []byte
	if err := db.QueryRow(ctx, `
		SELECT verification_token_hash FROM testagram_auth.identities WHERE user_id=$1::uuid
	`, userID).Scan(&rotatedVerificationHash); err != nil {
		t.Fatal(err)
	}
	if len(rotatedVerificationHash) != 32 || bytes.Equal(previousVerificationHash, rotatedVerificationHash) {
		t.Fatal("application-scoped resend did not rotate the stored email-verification capability")
	}

	resentEmail := <-mailDeliveries
	if resentEmail.recipient != applicantEmail || resentEmail.subject != "Verify your Testagram account" {
		t.Fatalf("resend verification email recipient/subject mismatch: %#v", resentEmail)
	}
	resentTokenMatch := tokenPattern.FindStringSubmatch(resentEmail.html)
	if len(resentTokenMatch) != 2 {
		t.Fatalf("resend email did not contain a verification capability: %q", resentEmail.html)
	}
	verifyToken := resentTokenMatch[1]
	if len(previousVerificationHash) != 32 || len(rotatedVerificationHash) != 32 ||
		bytes.Equal(previousVerificationHash, rotatedVerificationHash) ||
		!bytes.Equal(previousVerificationHash, authTokenHash(initialVerificationToken)) ||
		!bytes.Equal(rotatedVerificationHash, authTokenHash(verifyToken)) {
		t.Fatal("initial delivery/resend did not persist and rotate the exact emailed verification capabilities")
	}
	if len(mailDeliveries) != 0 {
		t.Fatalf("unexpected duplicate verification email deliveries: %d", len(mailDeliveries))
	}
	verifyReq := httptest.NewRequest(http.MethodPost, "/api/identity/applications/"+applicationID+"/verify-email",
		strings.NewReader(fmt.Sprintf("{\"token\":%q}", verifyToken)))
	verifyResp := httptest.NewRecorder()
	router.ServeHTTP(verifyResp, verifyReq)
	if verifyResp.Code != http.StatusOK {
		t.Fatalf("native email verification status=%d body=%s", verifyResp.Code, verifyResp.Body.String())
	}
	// Once the single-use email capability has activated both records,
	// the same identity must be able to obtain its first native session.
	session, err := issueNativeSession(ctx, db, userID, nativeEmail, tokenVersion, "native-ci", "127.0.0.1")
	if err != nil {
		t.Fatalf("verified approved identity could not obtain a native session: %v", err)
	}
	if session.AccessToken == "" || session.RefreshToken == "" || session.ExpiresIn != 900 {
		t.Fatalf("native session response is incomplete: %#v", session)
	}

	var activeCount, verifiedCount int
	if err := db.QueryRow(ctx, `
		SELECT count(*) FROM testagram_identity.native_registration_provisioning
		WHERE application_id=$1::uuid AND user_id=$2::uuid AND status='active' AND verified_at IS NOT NULL
	`, applicationID, userID).Scan(&activeCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `
		SELECT count(*) FROM testagram_auth.identities
		WHERE user_id=$1::uuid AND email_verified_at IS NOT NULL
		  AND verification_token_hash IS NULL AND verification_expires_at IS NULL
	`, userID).Scan(&verifiedCount); err != nil {
		t.Fatal(err)
	}
	if activeCount != 1 || verifiedCount != 1 {
		t.Fatalf("verification state active=%d identity_verified=%d", activeCount, verifiedCount)
	}

	var finalApplicationStatus string
	if err := db.QueryRow(ctx, `
		SELECT status FROM testagram_identity.identity_applications WHERE id=$1::uuid
	`, applicationID).Scan(&finalApplicationStatus); err != nil {
		t.Fatal(err)
	}
	if finalApplicationStatus != "verified" {
		t.Fatalf("application status after native email verification=%q; want verified", finalApplicationStatus)
	}

	tokens, err := issueNativeSession(ctx, db, userID, nativeEmail, tokenVersion, "native-ci", "127.0.0.1")
	if err != nil {
		t.Fatalf("native session after approved email verification: %v", err)
	}
	claims, sessionStatus, sessionErr := verifyNativeAccessToken(ctx, db, tokens.AccessToken)
	if sessionErr != nil || sessionStatus != http.StatusOK || claims.Subject != userID {
		t.Fatalf("native session verification subject=%q status=%d err=%v", claims.Subject, sessionStatus, sessionErr)
	}

	activeLogin := httptest.NewRequest(http.MethodPost, "/api/auth/login",
		strings.NewReader("{\"email\":\""+nativeEmail+"\",\"password\":\"native-ci-password-123\"}"))
	activeLoginResp := httptest.NewRecorder()
	if !svc.authRespond(activeLoginResp, activeLogin, "login") ||
		activeLoginResp.Code != http.StatusOK ||
		!strings.Contains(activeLoginResp.Body.String(), "access_token") {
		t.Fatalf("approved verified identity login status=%d body=%s", activeLoginResp.Code, activeLoginResp.Body.String())
	}
	if _, err := db.Exec(ctx, `
		UPDATE testagram_identity.native_registration_provisioning
		SET status='disabled',updated_at=now()
		WHERE application_id=$1::uuid AND user_id=$2::uuid AND status='active'
	`, applicationID, userID); err != nil {
		t.Fatal(err)
	}
	_, disabledStatus, disabledErr := verifyNativeAccessToken(ctx, db, tokens.AccessToken)
	if disabledErr == nil || disabledStatus != http.StatusUnauthorized {
		t.Fatalf("disabled provisioning retained native access: status=%d err=%v", disabledStatus, disabledErr)
	}
	if _, err := svc.rotateNativeRefresh(ctx, tokens.RefreshToken, "native-ci", "127.0.0.1"); err == nil {
		t.Fatal("disabled provisioning retained refresh-token access")
	}

	replayVerifyReq := httptest.NewRequest(http.MethodPost, "/api/identity/applications/"+applicationID+"/verify-email",
		strings.NewReader(fmt.Sprintf("{\"token\":%q}", verifyToken)))
	replayVerifyReq.SetPathValue("application_id", applicationID)
	replayVerifyResp := httptest.NewRecorder()
	svc.identityProvisioningVerifyEmail(replayVerifyResp, replayVerifyReq)
	if replayVerifyResp.Code != http.StatusBadRequest {
		t.Fatalf("verification token replay status=%d body=%s; want 400", replayVerifyResp.Code, replayVerifyResp.Body.String())
	}

	authReq := httptest.NewRequest(http.MethodPost, "/api/auth/signup",
		strings.NewReader("{\"email\":\"bypass@example.test\",\"username\":\"bypass_user\",\"password\":\"native-ci-password-123\"}"))
	authResp := httptest.NewRecorder()
	if !svc.authRespond(authResp, authReq, "signup") ||
		authResp.Code != http.StatusServiceUnavailable ||
		!strings.Contains(authResp.Body.String(), "IDENTITY_REVIEW_REQUIRED") {
		t.Fatalf("ordinary signup bypassed identity review: status=%d body=%s", authResp.Code, authResp.Body.String())
	}
	// Existing native identities may predate the approved-registration
	// ledger. They still require verified email, an enabled account and no
	// forced password reset, but must not be blocked solely because no
	// provisioning row exists.
	existingSuffix, err := authRandomToken()
	if err != nil {
		t.Fatal(err)
	}
	existingSuffix = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(existingSuffix[:16], "-", ""), "_", ""))
	existingEmail := "existing-native-" + existingSuffix + "@example.test"
	existingUsername := "legacy_" + existingSuffix
	existingPasswordHash, err := authPasswordHash("ExistingNativePassword-123!")
	if err != nil {
		t.Fatal(err)
	}
	var existingUserID string
	if err := db.QueryRow(ctx, `
		INSERT INTO testagram_auth.identities(email,username,password_hash,email_verified_at)
		VALUES($1,$2,$3,now())
		RETURNING user_id::text
	`, existingEmail, existingUsername, existingPasswordHash).Scan(&existingUserID); err != nil {
		t.Fatal(err)
	}
	var existingProvisioningRows int
	if err := db.QueryRow(ctx, `
		SELECT count(*) FROM testagram_identity.native_registration_provisioning WHERE user_id=$1::uuid
	`, existingUserID).Scan(&existingProvisioningRows); err != nil {
		t.Fatal(err)
	}
	if existingProvisioningRows != 0 {
		t.Fatalf("existing native identity unexpectedly has %d registration provisioning rows", existingProvisioningRows)
	}
	var existingTokenVersion int64
	if err := db.QueryRow(ctx, `
		SELECT token_version FROM testagram_auth.identities WHERE user_id=$1::uuid
	`, existingUserID).Scan(&existingTokenVersion); err != nil {
		t.Fatal(err)
	}
	existingTokens, err := issueNativeSession(ctx, db, existingUserID, existingEmail, existingTokenVersion, "native-ci", "127.0.0.1")
	if err != nil {
		t.Fatalf("verified existing native identity without a provisioning row could not sign in: %v", err)
	}
	if existingTokens.AccessToken == "" || existingTokens.RefreshToken == "" {
		t.Fatal("existing native identity received an incomplete session")
	}
	existingClaims, existingStatus, existingErr := verifyNativeAccessToken(ctx, db, existingTokens.AccessToken)
	if existingErr != nil || existingStatus != http.StatusOK || existingClaims.Subject != existingUserID {
		t.Fatalf("existing native identity access token status=%d subject=%q err=%v", existingStatus, existingClaims.Subject, existingErr)
	}
	rotatedExistingTokens, err := svc.rotateNativeRefresh(ctx, existingTokens.RefreshToken, "native-ci", "127.0.0.1")
	if err != nil {
		t.Fatalf("existing native identity refresh rotation failed: %v", err)
	}
	if _, status, err := verifyNativeAccessToken(ctx, db, rotatedExistingTokens.AccessToken); err != nil || status != http.StatusOK {
		t.Fatalf("existing native identity rotated access token status=%d err=%v", status, err)
	}

	existingLogin := httptest.NewRequest(http.MethodPost, "/api/auth/login",
		strings.NewReader("{\"email\":\""+existingEmail+"\",\"password\":\"ExistingNativePassword-123!\"}"))
	existingLoginResp := httptest.NewRecorder()
	if !svc.authRespond(existingLoginResp, existingLogin, "login") ||
		existingLoginResp.Code != http.StatusOK ||
		!strings.Contains(existingLoginResp.Body.String(), "access_token") {
		t.Fatalf("verified legacy native identity login status=%d body=%s", existingLoginResp.Code, existingLoginResp.Body.String())
	}
}
