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

// Legacy provider-specific account-linking integration tests were retired with the provider cutover.

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


func TestIdentityReviewerHonorsRevocationAndPermissionOverrides(t *testing.T) {
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

	const reviewerID = "ci-reviewer-permission"
	const reviewerEmail = "reviewer-permission@example.test"
	const project = "identity-permission-ci-project"
	const jwt = "identity-permission-ci-jwt"
	if _, err := db.Exec(ctx, `
		INSERT INTO testagram_auth.staff_roles(appwrite_user_id,role,access_revoked_at)
		VALUES ($1,'administrator',NULL)
		ON CONFLICT (appwrite_user_id) DO UPDATE
		SET role='administrator',access_revoked_at=NULL
	`, reviewerID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		DELETE FROM testagram_auth.staff_permission_overrides
		WHERE appwrite_user_id=$1 AND permission_key='identity.review'
	`, reviewerID); err != nil {
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
			"$id": reviewerID, "email": reviewerEmail,
			"emailVerification": true, "status": true,
		})
	}))
	defer server.Close()
	t.Setenv("APPWRITE_ENDPOINT", server.URL+"/v1")
	t.Setenv("APPWRITE_PROJECT_ID", project)

	svc := &service{db: db, upstream: server.Client()}
	authorize := func() (int, error) {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/identity/review-queue", nil)
		req.Header.Set("Authorization", "Bearer "+jwt)
		account, status, err := svc.identityReviewer(req)
		if err == nil && account.ID != reviewerID {
			t.Fatalf("reviewer identity=%q, want %q", account.ID, reviewerID)
		}
		return status, err
	}

	if status, err := authorize(); status != http.StatusOK || err != nil {
		t.Fatalf("active administrator reviewer status=%d err=%v; want 200", status, err)
	}
	if _, err := db.Exec(ctx, `
		UPDATE testagram_auth.staff_roles SET access_revoked_at=now()
		WHERE appwrite_user_id=$1
	`, reviewerID); err != nil {
		t.Fatal(err)
	}
	if status, err := authorize(); status != http.StatusForbidden || err == nil {
		t.Fatalf("revoked reviewer status=%d err=%v; want 403", status, err)
	}
	if _, err := db.Exec(ctx, `
		UPDATE testagram_auth.staff_roles SET access_revoked_at=NULL
		WHERE appwrite_user_id=$1
	`, reviewerID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO testagram_auth.staff_permission_overrides
		  (appwrite_user_id,permission_key,granted,reason,changed_by_appwrite_user_id)
		VALUES ($1,'identity.review',FALSE,'Explicit CI denial','ci-reviewer-one')
		ON CONFLICT (appwrite_user_id,permission_key) DO UPDATE
		SET granted=FALSE,reason=EXCLUDED.reason,changed_by_appwrite_user_id=EXCLUDED.changed_by_appwrite_user_id
	`, reviewerID); err != nil {
		t.Fatal(err)
	}
	if status, err := authorize(); status != http.StatusForbidden || err == nil {
		t.Fatalf("explicit deny override status=%d err=%v; want 403", status, err)
	}
	if _, err := db.Exec(ctx, `
		UPDATE testagram_auth.staff_roles SET role='support'
		WHERE appwrite_user_id=$1
	`, reviewerID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		UPDATE testagram_auth.staff_permission_overrides
		SET granted=TRUE,reason='Explicit CI grant'
		WHERE appwrite_user_id=$1 AND permission_key='identity.review'
	`, reviewerID); err != nil {
		t.Fatal(err)
	}
	if status, err := authorize(); status != http.StatusOK || err != nil {
		t.Fatalf("explicit grant override status=%d err=%v; want 200", status, err)
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

	const reviewerID = "ci-reviewer-three"
	const reviewerEmail = "reviewer-three@example.test"
	const applicantEmail = "reviewer-mismatch@example.test"
	const project = "identity-mismatch-ci-project"
	const jwt = "identity-mismatch-ci-jwt"
	if _, err := db.Exec(ctx, `
		INSERT INTO testagram_auth.staff_roles(appwrite_user_id,role,access_revoked_at)
		VALUES ($1,'administrator',NULL)
		ON CONFLICT (appwrite_user_id) DO UPDATE
		SET role='administrator',access_revoked_at=NULL
	`, reviewerID); err != nil {
		t.Fatal(err)
	}
	var applicationID, requestID string
	if err := db.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&applicationID); err != nil {
		t.Fatal(err)
	}
	fingerprint := strings.Repeat("b", 64)
	payload, err := json.Marshal(map[string]any{
		"application_id": applicationID,
		"email": applicantEmail,
		"username": "review_mismatch",
		"display_name": "Reviewer Mismatch",
		"evidence_fingerprint_sha256": fingerprint,
		"terms_version": "terms-v1",
		"privacy_version": "privacy-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `
		INSERT INTO testagram_auth.sensitive_action_requests
		  (action_type,requested_by_appwrite_user_id,target_reference,reason,payload,payload_sha256,required_approvals,status,expires_at)
		VALUES ('identity.approve','ci-intake-service',$1,
		        'Reviewer separation integration fixture',$2::jsonb,repeat('c',64),2,'pending',now()+interval '1 hour')
		RETURNING id::text
	`, applicationID, string(payload)).Scan(&requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO testagram_identity.identity_applications
		  (id,email,username,display_name,adult_confirmed_at,adult_verified_at,
		   adult_verification_provider,adult_verification_reference,adult_verification_checked_at,
		   evidence_reference,evidence_fingerprint_sha256,terms_version,privacy_version,
		   terms_accepted_at,privacy_accepted_at,status,sensitive_action_request_id)
		VALUES ($1::uuid,$2,'review_mismatch','Reviewer Mismatch',now(),now(),
		        'testagram_manual','review-mismatch-adult-ref',now(),
		        'opaque-review-mismatch-evidence',decode($3,'hex'),'terms-v1','privacy-v1',
		        now(),now(),'verified',$4::uuid)
	`, applicationID, applicantEmail, fingerprint, requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO testagram_identity.identity_application_reviews
		  (application_id,reviewer_appwrite_user_id,decision,reason,evidence_audit_reference)
		VALUES ($1::uuid,'ci-reviewer-one','approved','First independent evidence review','mismatch-audit-01'),
		       ($1::uuid,'ci-reviewer-two','approved','Second independent evidence review','mismatch-audit-02')
	`, applicationID); err != nil {
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
			"$id": reviewerID, "email": reviewerEmail,
			"emailVerification": true, "status": true,
		})
	}))
	defer server.Close()
	t.Setenv("APPWRITE_ENDPOINT", server.URL+"/v1")
	t.Setenv("APPWRITE_PROJECT_ID", project)

	svc := &service{db: db, upstream: server.Client()}
	req := httptest.NewRequest(http.MethodPost,
		"/api/admin/identity/applications/"+applicationID+"/approve",
		strings.NewReader(`{"reason":"Attempt by a non-reviewer to approve this application"}`))
	req.SetPathValue("application_id", applicationID)
	req.Header.Set("Authorization", "Bearer "+jwt)
	response := httptest.NewRecorder()
	svc.identityApplicationApprove(response, req)
	if response.Code != http.StatusForbidden ||
		!strings.Contains(response.Body.String(), "APPROVAL_REVIEWER_MISMATCH") {
		t.Fatalf("non-reviewer approval status=%d body=%s; want 403 reviewer mismatch", response.Code, response.Body.String())
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
		t.Fatalf("non-reviewer changed approval state: approvals=%d tokens=%d application=%q",
			approvalCount, tokenCount, applicationStatus)
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

	const project = "identity-concurrency-ci-project"
	const reviewerOneJWT = "identity-concurrency-reviewer-one-jwt"
	const reviewerTwoJWT = "identity-concurrency-reviewer-two-jwt"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/account" || r.Header.Get("X-Appwrite-Project") != project {
			http.Error(w, "invalid test Appwrite request", http.StatusUnauthorized)
			return
		}
		var id, email string
		switch r.Header.Get("X-Appwrite-JWT") {
		case reviewerOneJWT:
			id, email = "ci-reviewer-one", "reviewer-one@example.test"
		case reviewerTwoJWT:
			id, email = "ci-reviewer-two", "reviewer-two@example.test"
		default:
			http.Error(w, "invalid test Appwrite JWT", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"$id": id, "email": email, "emailVerification": true, "status": true,
		})
	}))
	defer server.Close()
	t.Setenv("APPWRITE_ENDPOINT", server.URL+"/v1")
	t.Setenv("APPWRITE_PROJECT_ID", project)
	svc := &service{db: db, upstream: server.Client()}

	createFixture := func(email, username, fingerprint string) (string, string) {
		t.Helper()
		var applicationID, requestID string
		if err := db.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&applicationID); err != nil {
			t.Fatal(err)
		}
		payload, err := json.Marshal(map[string]any{
			"application_id": applicationID,
			"email": email,
			"username": username,
			"display_name": "Concurrent Review",
			"evidence_fingerprint_sha256": fingerprint,
			"terms_version": "terms-v1",
			"privacy_version": "privacy-v1",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(ctx, `
			INSERT INTO testagram_auth.sensitive_action_requests
			  (action_type,requested_by_appwrite_user_id,target_reference,reason,payload,payload_sha256,required_approvals,status,expires_at)
			VALUES ('identity.approve','ci-intake-service',$1,
			        'Concurrent identity approval integration fixture',$2::jsonb,repeat('a',64),2,'pending',now()+interval '1 hour')
			RETURNING id::text
		`, applicationID, string(payload)).Scan(&requestID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `
			INSERT INTO testagram_identity.identity_applications
			  (id,email,username,display_name,adult_confirmed_at,adult_verified_at,
			   adult_verification_provider,adult_verification_reference,adult_verification_checked_at,
			   evidence_reference,evidence_fingerprint_sha256,terms_version,privacy_version,
			   terms_accepted_at,privacy_accepted_at,status,sensitive_action_request_id)
			VALUES ($1::uuid,$2,$3,'Concurrent Review',now(),now(),
			        'testagram_manual','concurrent-adult-ref',now(),
			        'opaque-concurrent-evidence-ref',decode($4,'hex'),'terms-v1','privacy-v1',
			        now(),now(),'verified',$5::uuid)
		`, applicationID, email, username, fingerprint, requestID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `
			INSERT INTO testagram_identity.identity_application_reviews
			  (application_id,reviewer_appwrite_user_id,decision,reason,evidence_audit_reference)
			VALUES ($1::uuid,'ci-reviewer-one','approved','First independent evidence review','concurrent-audit-01'),
			       ($1::uuid,'ci-reviewer-two','approved','Second independent evidence review','concurrent-audit-02')
		`, applicationID); err != nil {
			t.Fatal(err)
		}
		return applicationID, requestID
	}
	approve := func(applicationID, jwt string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost,
			"/api/admin/identity/applications/"+applicationID+"/approve",
			strings.NewReader(`{"reason":"Concurrent independent approval decision"}`))
		req.SetPathValue("application_id", applicationID)
		req.Header.Set("Authorization", "Bearer "+jwt)
		response := httptest.NewRecorder()
		svc.identityApplicationApprove(response, req)
		return response
	}

	// Two distinct reviewers racing on one application must serialize into
	// one pending response and one final approval/token issuance.
	applicationID, requestID := createFixture(
		"concurrent-review@example.test", "concurrent_review", strings.Repeat("e", 64),
	)
	results := make(chan *httptest.ResponseRecorder, 2)
	go func() { results <- approve(applicationID, reviewerOneJWT) }()
	go func() { results <- approve(applicationID, reviewerTwoJWT) }()
	first, second := <-results, <-results
	statuses := map[int]int{first.Code: 1}
	statuses[second.Code]++
	if statuses[http.StatusAccepted] != 1 || statuses[http.StatusOK] != 1 {
		t.Fatalf("concurrent distinct reviewers returned statuses %d and %d; want one 202 and one 200: %s | %s",
			first.Code, second.Code, first.Body.String(), second.Body.String())
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
		t.Fatalf("distinct-reviewer race state: approvals=%d tokens=%d application=%q request=%q",
			approvals, tokens, applicationStatus, requestStatus)
	}

	// Two simultaneous requests from the same reviewer must not count as two
	// independent approvals or mint a token.
	duplicateApplicationID, duplicateRequestID := createFixture(
		"duplicate-review@example.test", "duplicate_review", strings.Repeat("0", 64),
	)
	duplicateResults := make(chan *httptest.ResponseRecorder, 2)
	go func() { duplicateResults <- approve(duplicateApplicationID, reviewerOneJWT) }()
	go func() { duplicateResults <- approve(duplicateApplicationID, reviewerOneJWT) }()
	duplicateFirst, duplicateSecond := <-duplicateResults, <-duplicateResults
	duplicateStatuses := map[int]int{duplicateFirst.Code: 1}
	duplicateStatuses[duplicateSecond.Code]++
	if duplicateStatuses[http.StatusAccepted] != 1 || duplicateStatuses[http.StatusConflict] != 1 {
		t.Fatalf("duplicate reviewer race returned statuses %d and %d; want one 202 and one 409: %s | %s",
			duplicateFirst.Code, duplicateSecond.Code, duplicateFirst.Body.String(), duplicateSecond.Body.String())
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
		t.Fatalf("duplicate-reviewer race state: approvals=%d tokens=%d application=%q request=%q",
			approvals, tokens, applicationStatus, requestStatus)
	}
}
