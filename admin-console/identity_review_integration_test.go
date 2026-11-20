package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect to isolated PostgreSQL: %v", err)
	}
	defer conn.Close(context.Background())

	var evidenceReviewerOne, evidenceReviewerTwo, approverOne, approverTwo string
	if err := conn.QueryRow(ctx, "SELECT gen_random_uuid()::text,gen_random_uuid()::text,gen_random_uuid()::text,gen_random_uuid()::text").
		Scan(&evidenceReviewerOne, &evidenceReviewerTwo, &approverOne, &approverTwo); err != nil {
		t.Fatalf("generate native staff IDs: %v", err)
	}
	staff := []struct {
		id   string
		role string
	}{
		{evidenceReviewerOne, "administrator"},
		{evidenceReviewerTwo, "administrator"},
		{approverOne, "administrator"},
		{approverTwo, "administrator"},
	}
	for i, person := range staff {
		email := fmt.Sprintf("identity-review-staff-%d-%s@example.test", i, person.id[:8])
		username := fmt.Sprintf("reviewstaff_%d_%s", i, person.id[:8])
		if _, err := conn.Exec(ctx, `INSERT INTO testagram_auth.identities(user_id,email,username,password_hash,email_verified_at)
			VALUES($1::uuid,$2,$3,'test-only-invalid-password-hash',now())`, person.id, email, username); err != nil {
			t.Fatalf("insert native staff identity: %v", err)
		}
		if _, err := conn.Exec(ctx, `INSERT INTO testagram_auth.staff_principals(user_id,enabled,mfa_enrolled_at,enabled_at,enabled_by)
			VALUES($1::uuid,TRUE,now(),now(),$1::uuid)`, person.id); err != nil {
			t.Fatalf("enable native staff principal: %v", err)
		}
		if _, err := conn.Exec(ctx, `INSERT INTO testagram_auth.staff_role_grants(user_id,role,granted_by)
			VALUES($1::uuid,$2,$1::uuid)`, person.id, person.role); err != nil {
			t.Fatalf("grant native staff role: %v", err)
		}
	}

	var applicationID, requestID string
	if err := conn.QueryRow(ctx, "SELECT gen_random_uuid()::text, gen_random_uuid()::text").Scan(&applicationID, &requestID); err != nil {
		t.Fatalf("generate test IDs: %v", err)
	}
	fingerprint := sha256.Sum256([]byte("identity-review:" + applicationID))
	username := "identity_review_ci_" + applicationID[:8]
	email := "identity-review-" + applicationID[:8] + "@example.test"
	payload := identityReviewSnapshot{
		ApplicationID: applicationID,
		Email: email,
		Username: username,
		DisplayName: "Identity Review CI",
		EvidenceFingerprintSHA256: hex.EncodeToString(fingerprint[:]),
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
		INSERT INTO testagram_auth.native_sensitive_action_requests
		  (id,action_type,requested_by_user_id,requested_by_service,target_reference,
		   reason,payload,payload_sha256,required_approvals)
		VALUES ($1::uuid,'identity.approve',NULL,'identity_pipeline',$2,
		        'CI identity review request',$3::jsonb,$4,2)
	`, requestID, applicationID, string(rawPayload), hex.EncodeToString(digest[:]))
	if err != nil {
		t.Fatalf("insert native identity-pipeline request: %v", err)
	}

	_, err = conn.Exec(ctx, `
		INSERT INTO testagram_identity.identity_applications
		  (id,email,username,display_name,adult_confirmed_at,adult_verified_at,
		   adult_verification_provider,adult_verification_reference,adult_verification_checked_at,
		   evidence_reference,evidence_fingerprint_sha256,terms_version,privacy_version,
		   terms_accepted_at,privacy_accepted_at,status,native_sensitive_action_request_id)
		VALUES ($1::uuid,$2,$3,$4,now(),now(),'testagram_manual','testagram_manual-verification-' || $3,now(),
		        'opaque-evidence-reference-ci',$5,$6,$7,now(),now(),'verified',$8::uuid)
	`, applicationID, payload.Email, payload.Username, payload.DisplayName,
		fingerprint[:], payload.TermsVersion, payload.PrivacyVersion, requestID)
	if err != nil {
		t.Fatalf("insert verified identity application: %v", err)
	}

	// The database token trigger requires a completed, verified age-assurance
	// session, not merely an adult_verified_at marker on the application.
	var verificationSessionID string
	if err := conn.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&verificationSessionID); err != nil {
		t.Fatalf("generate verified evidence session ID: %v", err)
	}
	sessionTokenHash := sha256.Sum256([]byte("identity-review-session:" + applicationID))
	_, err = conn.Exec(ctx, `
		INSERT INTO testagram_identity.verification_sessions
		  (id,application_id,session_token_sha256,purpose,requested_minimum_age,status,
		   evidence_reference,evidence_fingerprint_sha256,assurance_method,assurance_level,
		   verified_age_over_threshold,expires_at,submitted_at,decided_at,decided_by_user_id)
		VALUES($1::uuid,$2::uuid,$3,'age_assurance',18,'verified',
		       'opaque-evidence-reference-ci',$4,'document_review','reviewed',
		       TRUE,now()+interval '1 day',now(),now(),$5::uuid)
	`, verificationSessionID, applicationID, sessionTokenHash[:], fingerprint[:], evidenceReviewerOne)
	if err != nil {
		t.Fatalf("insert completed verified age-assurance session: %v", err)
	}

	// The evidence-review pair and account-approval pair must be disjoint.
	_, err = conn.Exec(ctx, `
		INSERT INTO testagram_identity.identity_application_reviews
		  (application_id,reviewer_user_id,review_stage,decision,reason,evidence_audit_reference)
		VALUES ($1::uuid,$2::uuid,'evidence_review','approved','First evidence review',$4),
		       ($1::uuid,$3::uuid,'evidence_review','approved','Second evidence review',$4)
	`, applicationID, evidenceReviewerOne, evidenceReviewerTwo, payload.EvidenceAuditReference)
	if err != nil {
		t.Fatalf("insert independent evidence reviews: %v", err)
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin evidence-reviewer rejection transaction: %v", err)
	}
	if _, _, _, err := recordSensitiveActionDecision(ctx, tx, requestID, "identity.approve", "", evidenceReviewerOne, "approved", "Evidence reviewer cannot approve account"); err == nil {
		_ = tx.Rollback(ctx)
		t.Fatal("evidence reviewer was allowed to approve account activation")
	}
	_ = tx.Rollback(ctx)

	tx, err = conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin first account-approval transaction: %v", err)
	}
	state, applicationState, approvalCount, err := recordSensitiveActionDecision(ctx, tx, requestID, "identity.approve", "", approverOne, "approved", "First independent account approval")
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("record first two-person decision: %v", err)
	}
	if state != "pending" || applicationState != "under_review" || approvalCount != 1 {
		_ = tx.Rollback(ctx)
		t.Fatalf("first decision state=%q application=%q approvals=%d; want pending/under_review/1", state, applicationState, approvalCount)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit first account approval: %v", err)
	}

	tx, err = conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin duplicate account-approval transaction: %v", err)
	}
	if _, _, _, err := recordSensitiveActionDecision(ctx, tx, requestID, "identity.approve", "", approverOne, "approved", "Duplicate account approval"); err == nil {
		_ = tx.Rollback(ctx)
		t.Fatal("same approver was counted twice")
	}
	_ = tx.Rollback(ctx)

	tx, err = conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin second account-approval transaction: %v", err)
	}
	state, applicationState, approvalCount, err = recordSensitiveActionDecision(ctx, tx, requestID, "identity.approve", "", approverTwo, "approved", "Second independent account approval")
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("record second two-person decision and provisioning grant: %v", err)
	}
	if state != "approved" || applicationState != "approved" || approvalCount != 2 {
		_ = tx.Rollback(ctx)
		t.Fatalf("second decision state=%q application=%q approvals=%d; want approved/approved/2", state, applicationState, approvalCount)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit second account approval: %v", err)
	}

	var finalStatus, requestStatus string
	var evidenceReviewCount, accountReviewCount, approvalCountFinal, tokenCount int
	err = conn.QueryRow(ctx, `
		SELECT a.status,sar.status,
		       (SELECT count(*) FROM testagram_identity.identity_application_reviews r
		        WHERE r.application_id=a.id AND r.review_stage='evidence_review' AND r.decision='approved'),
		       (SELECT count(*) FROM testagram_identity.identity_application_reviews r
		        WHERE r.application_id=a.id AND r.review_stage='account_approval' AND r.decision='approved'),
		       (SELECT count(DISTINCT approver_user_id) FROM testagram_auth.native_sensitive_action_approvals sa
		        WHERE sa.request_id=sar.id AND sa.decision='approved'),
		       (SELECT count(*) FROM testagram_identity.identity_approval_tokens t
		        WHERE t.application_id=a.id AND t.first_approver_user_id IS NOT NULL AND t.second_approver_user_id IS NOT NULL)
		FROM testagram_identity.identity_applications a
		JOIN testagram_auth.native_sensitive_action_requests sar ON sar.id=a.native_sensitive_action_request_id
		WHERE a.id=$1::uuid
	`, applicationID).Scan(&finalStatus, &requestStatus, &evidenceReviewCount, &accountReviewCount, &approvalCountFinal, &tokenCount)
	if err != nil {
		t.Fatalf("read final application and approval state: %v", err)
	}
	if finalStatus != "approved" || requestStatus != "approved" || evidenceReviewCount != 2 ||
		accountReviewCount != 2 || approvalCountFinal != 2 || tokenCount != 1 {
		t.Fatalf("final application=%q request=%q evidence_reviews=%d account_reviews=%d approvals=%d tokens=%d; want approved/approved/2/2/2/1",
			finalStatus, requestStatus, evidenceReviewCount, accountReviewCount, approvalCountFinal, tokenCount)
	}
}
