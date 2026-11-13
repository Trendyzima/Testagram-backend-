package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestRecordIdentityApplicationReviewIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect to isolated PostgreSQL: %v", err)
	}
	defer conn.Close(context.Background())

	reviewerOne := "integration-reviewer-one"
	reviewerTwo := "integration-reviewer-two"
	requester := "integration-intake-service"
	_, err = conn.Exec(ctx, `
		INSERT INTO testagram_auth.staff_roles(appwrite_user_id, role)
		VALUES ($1, 'administrator'), ($2, 'administrator'), ($3, 'operations')
		ON CONFLICT (appwrite_user_id) DO NOTHING
	`, reviewerOne, reviewerTwo, requester)
	if err != nil {
		t.Fatalf("insert test staff principals: %v", err)
	}

	var applicationID, requestID string
	if err := conn.QueryRow(ctx, "SELECT gen_random_uuid()::text, gen_random_uuid()::text").Scan(&applicationID, &requestID); err != nil {
		t.Fatalf("generate test IDs: %v", err)
	}

	fingerprint := make([]byte, 32)
	for i := range fingerprint {
		fingerprint[i] = byte(i + 1)
	}
	payload := identityReviewSnapshot{
		ApplicationID: applicationID,
		Email: "identity-review@example.test",
		Username: "identity_review_ci",
		DisplayName: "Identity Review CI",
		EvidenceFingerprintSHA256: hex.EncodeToString(fingerprint),
		EvidenceAuditReference: "idswyft-audit-reference-ci-001",
		TermsVersion: "terms-ci-v1",
		PrivacyVersion: "privacy-ci-v1",
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal review snapshot: %v", err)
	}
	digest := sha256.Sum256(rawPayload)

	_, err = conn.Exec(ctx, `
		INSERT INTO testagram_auth.sensitive_action_requests
		  (id, action_type, requested_by_appwrite_user_id, target_reference,
		   reason, payload, payload_sha256, required_approvals)
		VALUES ($1::uuid, 'identity.approve', $2, $3,
		        'CI identity review request', $4::jsonb, $5, 2)
	`, requestID, requester, applicationID, string(rawPayload), hex.EncodeToString(digest[:]))
	if err != nil {
		t.Fatalf("insert sensitive-action request: %v", err)
	}

	_, err = conn.Exec(ctx, `
		INSERT INTO testagram_identity.identity_applications
		  (id, email, username, display_name, adult_confirmed_at, adult_verified_at,
		   adult_verification_provider, adult_verification_reference, adult_verification_checked_at,
		   adult_verification_session_id, adult_verification_mode, adult_verification_threshold,
		   adult_verification_result, adult_verification_webhook_received_at,
		   evidence_reference, evidence_fingerprint_sha256, terms_version, privacy_version,
		   terms_accepted_at, privacy_accepted_at, sensitive_action_request_id)
		VALUES ($1::uuid, $2, $3, $4, now(), now(), 'idswyft', 'idswyft-verification-ci-001', now(),
		        '223e4567-e89b-42d3-a456-426614174000', 'age_only', 18, 'verified', now(),
		        'opaque-evidence-reference-ci', $5, $6, $7, now(), now(), $8::uuid)
	`, applicationID, payload.Email, payload.Username, payload.DisplayName,
		fingerprint, payload.TermsVersion, payload.PrivacyVersion, requestID)
	if err != nil {
		t.Fatalf("insert verified identity application: %v", err)
	}

	_, err = conn.Exec(ctx, `
		INSERT INTO testagram_auth.sensitive_action_approvals
		  (request_id, approver_appwrite_user_id, decision, reason)
		VALUES ($1::uuid, $2, 'approved', 'First independent reviewer approval')
	`, requestID, reviewerOne)
	if err != nil {
		t.Fatalf("insert first approval: %v", err)
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin first review transaction: %v", err)
	}
	state, err := recordIdentityApplicationReview(ctx, tx, requestID, applicationID, reviewerOne, "approved",
		"First reviewer confirms the verified evidence", rawPayload, 1)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("record first identity review: %v", err)
	}
	if state != "under_review" {
		_ = tx.Rollback(ctx)
		t.Fatalf("first approval state = %q, want under_review", state)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit first identity review: %v", err)
	}

	_, err = conn.Exec(ctx, `
		INSERT INTO testagram_auth.sensitive_action_approvals
		  (request_id, approver_appwrite_user_id, decision, reason)
		VALUES ($1::uuid, $2, 'approved', 'Second independent reviewer approval')
	`, requestID, reviewerTwo)
	if err != nil {
		t.Fatalf("insert second approval: %v", err)
	}
	_, err = conn.Exec(ctx, `
		UPDATE testagram_auth.sensitive_action_requests
		SET status='approved', decided_at=now()
		WHERE id=$1::uuid
	`, requestID)
	if err != nil {
		t.Fatalf("complete two-person approval request: %v", err)
	}

	tx, err = conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin second review transaction: %v", err)
	}
	state, err = recordIdentityApplicationReview(ctx, tx, requestID, applicationID, reviewerTwo, "approved",
		"Second reviewer independently confirms the evidence", rawPayload, 2)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("record second identity review: %v", err)
	}
	if state != "approved" {
		_ = tx.Rollback(ctx)
		t.Fatalf("second approval state = %q, want approved", state)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit second identity review: %v", err)
	}

	var finalStatus string
	var reviewCount int
	err = conn.QueryRow(ctx, `
		SELECT a.status,
		       (SELECT count(*) FROM testagram_identity.identity_application_reviews r
		        WHERE r.application_id=a.id AND r.decision='approved')
		FROM testagram_identity.identity_applications a
		WHERE a.id=$1::uuid
	`, applicationID).Scan(&finalStatus, &reviewCount)
	if err != nil {
		t.Fatalf("read final application state: %v", err)
	}
	if finalStatus != "approved" || reviewCount != 2 {
		t.Fatalf("final state=%q review_count=%d; want approved and 2", finalStatus, reviewCount)
	}
}
