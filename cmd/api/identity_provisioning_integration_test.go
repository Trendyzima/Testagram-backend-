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
	claims, status, err := verifyNativeAccessToken(ctx, db, session.AccessToken)
	if err != nil || status != http.StatusOK {
		t.Fatalf("verify native reviewer session status=%d err=%v", status, err)
	}
	claims.MFAAuthenticatedAt = time.Now().UTC().Unix()
	session.AccessToken, err = authSignJWTClaims(claims)
	if err != nil {
		t.Fatal(err)
	}
	return reviewerID, reviewerEmail, session.AccessToken
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
	if _, err := db.Exec(ctx, "INSERT INTO testagram_auth.sensitive_action_approvals(request_id,approver_user_id,decision,reason) VALUES($1::uuid,$2::uuid,'approved','First approval before expiry')", requestID, reviewerTwoID); err != nil {
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
	reviewerOneID, _, _ := nativeStaffReviewerFixture(t, ctx, db)
	reviewerTwoID, _, _ := nativeStaffReviewerFixture(t, ctx, db)
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
		var applicationID, requestID, sessionID string
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
		if err := db.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&sessionID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `
			INSERT INTO testagram_identity.verification_sessions
			  (id,application_id,session_token_sha256,purpose,requested_minimum_age,status,
			   evidence_reference,evidence_fingerprint_sha256,assurance_method,assurance_level,
			   verified_age_over_threshold,expires_at,submitted_at,decided_at,decided_by_user_id)
			VALUES($1::uuid,$2::uuid,$3,'age_assurance',18,'verified',
			       'opaque-concurrent-evidence-ref',decode($4,'hex'),'document_review','reviewed',
			       TRUE,now()+interval '1 day',now(),now(),$5::uuid)
		`, sessionID, applicationID, authTokenHash("concurrent-session:"+applicationID), fingerprint, reviewerOneID); err != nil {
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
