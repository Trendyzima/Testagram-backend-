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
	username := "identity_review_ci_" + applicationID[:8]
	payload := identityReviewSnapshot{
		ApplicationID: applicationID,
		Email: "identity-review@example.test",
		Username: username,
		DisplayName: "Identity Review CI",
		EvidenceFingerprintSHA256: hex.EncodeToString(fingerprint),
		EvidenceAuditReference: "testagram_manual-audit-reference-ci-001",
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
		   evidence_reference, evidence_fingerprint_sha256, terms_version, privacy_version,
		   terms_accepted_at, privacy_accepted_at, sensitive_action_request_id)
		VALUES ($1::uuid, $2, $3, $4, now(), now(), 'testagram_manual', 'testagram_manual-verification-ci-001', now(),
		        'opaque-evidence-reference-ci', $5, $6, $7, now(), now(), $8::uuid)
	`, applicationID, payload.Email, payload.Username, payload.DisplayName,
		fingerprint, payload.TermsVersion, payload.PrivacyVersion, requestID)
	if err != nil {
		t.Fatalf("insert verified identity application: %v", err)
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin requester self-approval transaction: %v", err)
	}
	if _, _, _, err := recordSensitiveActionDecision(ctx, tx, requestID, requester, "approved", "Requester must not approve own request"); err == nil {
		_ = tx.Rollback(ctx)
		t.Fatal("requester was allowed to approve their own identity application")
	}
	_ = tx.Rollback(ctx)

	tx, err = conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin first reviewer transaction: %v", err)
	}
	state, applicationState, approvalCount, err := recordSensitiveActionDecision(ctx, tx, requestID, reviewerOne, "approved", "First independent reviewer approval")
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("record first two-person decision: %v", err)
	}
	if state != "pending" || applicationState != "under_review" || approvalCount != 1 {
		_ = tx.Rollback(ctx)
		t.Fatalf("first decision state=%q application=%q approvals=%d; want pending/under_review/1", state, applicationState, approvalCount)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit first reviewer decision: %v", err)
	}

	tx, err = conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin duplicate reviewer transaction: %v", err)
	}
	if _, _, _, err := recordSensitiveActionDecision(ctx, tx, requestID, reviewerOne, "approved", "Duplicate first reviewer decision"); err == nil {
		_ = tx.Rollback(ctx)
		t.Fatal("same reviewer was counted twice")
	}
	_ = tx.Rollback(ctx)

	tx, err = conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin second reviewer transaction: %v", err)
	}
	state, applicationState, approvalCount, err = recordSensitiveActionDecision(ctx, tx, requestID, reviewerTwo, "approved", "Second independent reviewer approval")
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("record second two-person decision: %v", err)
	}
	if state != "approved" || applicationState != "approved" || approvalCount != 2 {
		_ = tx.Rollback(ctx)
		t.Fatalf("second decision state=%q application=%q approvals=%d; want approved/approved/2", state, applicationState, approvalCount)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit second reviewer decision: %v", err)
	}

	var finalStatus, requestStatus string
	var reviewCount, approvalCountFinal int
	err = conn.QueryRow(ctx, `
		SELECT a.status, sar.status,
		       (SELECT count(*) FROM testagram_identity.identity_application_reviews r
		        WHERE r.application_id=a.id AND r.decision='approved'),
		       (SELECT count(DISTINCT approver_appwrite_user_id) FROM testagram_auth.sensitive_action_approvals sa
		        WHERE sa.request_id=sar.id AND sa.decision='approved')
		FROM testagram_identity.identity_applications a
		JOIN testagram_auth.sensitive_action_requests sar ON sar.id=a.sensitive_action_request_id
		WHERE a.id=$1::uuid
	`, applicationID).Scan(&finalStatus, &requestStatus, &reviewCount, &approvalCountFinal)
	if err != nil {
		t.Fatalf("read final application and approval state: %v", err)
	}
	if finalStatus != "approved" || requestStatus != "approved" || reviewCount != 2 || approvalCountFinal != 2 {
		t.Fatalf("final application=%q request=%q reviews=%d approvals=%d; want approved/approved/2/2",
			finalStatus, requestStatus, reviewCount, approvalCountFinal)
	}
}
