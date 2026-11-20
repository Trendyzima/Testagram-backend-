package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/go-webauthn/webauthn/webauthn"
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
		{evidenceReviewerOne, "identity_reviewer"},
		{evidenceReviewerTwo, "identity_reviewer"},
		{approverOne, "administrator"},
		{approverTwo, "administrator"},
	}
	t.Setenv("TESTAGRAM_WEBAUTHN_RPID", "ops.example.test")
	t.Setenv("TESTAGRAM_WEBAUTHN_ORIGINS", "https://ops.example.test")
	t.Setenv("TESTAGRAM_AUTH_JWT_SECRET", strings.Repeat("testagram-admin-webauthn-readiness-key-", 2))
	t.Setenv("TESTAGRAM_WEBAUTHN_READINESS_TOKEN", strings.Repeat("testagram-admin-readiness-token-", 2))
	admin := testApp(t)
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

		if _, err := conn.Exec(ctx, `
			INSERT INTO testagram_auth.staff_webauthn_users(rpid,user_id,user_handle)
			VALUES('ops.example.test',$1::uuid,decode(md5($1::text),'hex'))
		`, person.id); err != nil {
			t.Fatalf("insert staff WebAuthn user: %v", err)
		}
		credentialID := []byte("credential-" + person.id)
		encryptedCredential, err := admin.encryptStaffCredential(&webauthn.Credential{
			ID: credentialID,
			PublicKey: []byte("test-public-key"),
		})
		if err != nil {
			t.Fatalf("encrypt staff WebAuthn credential fixture: %v", err)
		}
		if _, err := conn.Exec(ctx, `
			INSERT INTO testagram_auth.staff_webauthn_credentials
			  (rpid,user_id,credential_id,credential,user_verified,clone_warning)
			VALUES('ops.example.test',$1::uuid,$2,$3,TRUE,FALSE)
		`, person.id, credentialID, encryptedCredential); err != nil {
			t.Fatalf("insert verified staff WebAuthn credential: %v", err)
		}
	}

	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer health.Close()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open Admin Console readiness pool: %v", err)
	}
	defer pool.Close()
	admin.database = pool
	admin.apiHealthURL = health.URL
	admin.client = health.Client()
	readiness := admin.staffWebAuthnReadiness(ctx)
	if ready, _ := readiness["identity_review_ready"].(bool); !ready {
		t.Fatalf("Admin Console rejected complete PostgreSQL WebAuthn readiness: %#v", readiness)
	}
	// Dual-role staff must not satisfy both independent reviewer and approver
	// groups, even when four credentialed staff accounts exist.
	if _, err := conn.Exec(ctx, `
		INSERT INTO testagram_auth.staff_role_grants(user_id,role,granted_by)
		VALUES($1::uuid,'administrator',$2::uuid)
	`, evidenceReviewerOne, approverOne); err != nil {
		t.Fatalf("add overlapping staff role fixture: %v", err)
	}
	readiness = admin.staffWebAuthnReadiness(ctx)
	if ready, _ := readiness["identity_review_ready"].(bool); ready {
		t.Fatalf("Admin Console counted a dual-role staff member in both independent groups: %#v", readiness)
	}
	if _, err := conn.Exec(ctx, `
		DELETE FROM testagram_auth.staff_role_grants
		WHERE user_id=$1::uuid AND role='administrator'
	`, evidenceReviewerOne); err != nil {
		t.Fatalf("remove overlapping staff role fixture: %v", err)
	}
	readiness = admin.staffWebAuthnReadiness(ctx)
	if ready, _ := readiness["identity_review_ready"].(bool); !ready {
		t.Fatalf("Admin Console did not recover after removing overlapping role: %#v", readiness)
	}
	readinessRequest := httptest.NewRequest(http.MethodGet, "/internal/webauthn/readiness", nil)
	readinessRequest.Header.Set("Authorization", "Bearer "+os.Getenv("TESTAGRAM_WEBAUTHN_READINESS_TOKEN"))
	readinessResponse := httptest.NewRecorder()
	admin.handleInternalWebAuthnReadiness(readinessResponse, readinessRequest)
	if readinessResponse.Code != http.StatusOK || !strings.Contains(readinessResponse.Body.String(), `"identity_review_ready":true`) {
		t.Fatalf("Admin Console did not attest ready state: status=%d body=%s", readinessResponse.Code, readinessResponse.Body.String())
	}
	if _, err := conn.Exec(ctx, "UPDATE testagram_auth.staff_webauthn_credentials SET revoked_at=now() WHERE user_id=$1::uuid", approverTwo); err != nil {
		t.Fatal(err)
	}
	readiness = admin.staffWebAuthnReadiness(ctx)
	if ready, _ := readiness["identity_review_ready"].(bool); ready {
		t.Fatalf("Admin Console remained ready after an approver credential was revoked: %#v", readiness)
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
	if _, err := conn.Exec(ctx, `
		INSERT INTO testagram_identity.verification_reviews
		  (session_id,reviewer_user_id,decision,reason_code,evidence_reference)
		VALUES ($1::uuid,$2::uuid,'verified','manual_review_passed','opaque-evidence-reference-ci'),
		       ($1::uuid,$3::uuid,'verified','manual_review_passed','opaque-evidence-reference-ci')
	`, verificationSessionID, evidenceReviewerOne, evidenceReviewerTwo); err != nil {
		t.Fatalf("insert independent age-assurance reviews: %v", err)
	}
	var matchingEvidenceSessions int
	if err := conn.QueryRow(ctx, `
		SELECT count(*)
		FROM testagram_identity.verification_sessions v
		JOIN testagram_identity.identity_applications a ON a.id=v.application_id
		WHERE a.id=$1::uuid AND v.purpose='age_assurance' AND v.status='verified'
		  AND v.verified_age_over_threshold IS TRUE
		  AND v.assurance_level IN ('reviewed','substantial','high')
		  AND v.evidence_reference=a.evidence_reference
		  AND v.evidence_fingerprint_sha256=a.evidence_fingerprint_sha256
	`, applicationID).Scan(&matchingEvidenceSessions); err != nil {
		t.Fatalf("verify matching age-assurance evidence: %v", err)
	}
	if matchingEvidenceSessions != 1 {
		t.Fatalf("matching verified age-assurance sessions=%d; want 1", matchingEvidenceSessions)
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

	// Expiry must be checked inside the locked decision transaction, not only
	// by the HTTP preflight. This closes the race where a request expires after
	// the console has loaded it but before the reviewer submits the decision.
	var expiredRequestID string
	if err := conn.QueryRow(ctx, `
		INSERT INTO testagram_auth.native_sensitive_action_requests
		  (action_type,requested_by_user_id,requested_by_service,target_reference,
		   reason,payload,payload_sha256,required_approvals,created_at,expires_at)
		VALUES ('identity.approve',NULL,'identity_pipeline',$1,
		        'Expired approval integration fixture','{}'::jsonb,repeat('d',64),2,
		        now()-interval '2 minutes',now()-interval '1 minute')
		RETURNING id::text
	`, applicationID).Scan(&expiredRequestID); err != nil {
		t.Fatalf("insert expired approval request: %v", err)
	}
	tx, err = conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin expired approval transaction: %v", err)
	}
	if _, _, _, err := recordSensitiveActionDecision(ctx, tx, expiredRequestID, "identity.approve", "", approverOne, "approved", "Expired approval must be rejected"); err == nil {
		_ = tx.Rollback(ctx)
		t.Fatal("expired identity approval request was accepted")
	}
	_ = tx.Rollback(ctx)
	var expiredApprovalCount int
	if err := conn.QueryRow(ctx, `
		SELECT count(*) FROM testagram_auth.native_sensitive_action_approvals
		WHERE request_id=$1::uuid
	`, expiredRequestID).Scan(&expiredApprovalCount); err != nil {
		t.Fatalf("read expired approval ledger: %v", err)
	}
	if expiredApprovalCount != 0 {
		t.Fatalf("expired request recorded %d approval decisions; want 0", expiredApprovalCount)
	}
}
