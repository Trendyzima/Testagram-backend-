package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIdswyftVerifiedWebhookCreatesOneTwoPersonReviewRequest(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect to isolated PostgreSQL: %v", err)
	}
	defer db.Close()

	const secret = "ci-only-idswyft-webhook-secret-0123456789"
	t.Setenv("IDSWYFT_WEBHOOK_SECRET", secret)
	var applicationID, verificationID string
	if err := db.QueryRow(ctx, "SELECT gen_random_uuid()::text, gen_random_uuid()::text").Scan(&applicationID, &verificationID); err != nil {
		t.Fatalf("generate IDs: %v", err)
	}
	_, err = db.Exec(ctx, `
		INSERT INTO testagram_identity.identity_applications
		  (id,email,username,display_name,adult_confirmed_at,evidence_reference,
		   terms_version,privacy_version,terms_accepted_at,privacy_accepted_at,
		   adult_verification_session_id,adult_verification_mode,
		   adult_verification_threshold,adult_verification_result)
		VALUES ($1::uuid,'webhook-applicant@example.test','webhook_applicant',
		        'Webhook Applicant',now(),'idswyft-session-pending:'||$1,
		        'terms-ci-v1','privacy-ci-v1',now(),now(),
		        $2,'age_only',18,'pending')
	`, applicationID, verificationID)
	if err != nil {
		t.Fatalf("insert pending identity application: %v", err)
	}

	payload := map[string]any{
		"event": "verification.completed",
		"user_id": applicationID,
		"verification_id": verificationID,
		"status": "verified",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"data": map[string]any{"sensitive_payload_marker": "do-not-persist-raw-provider-payload"},
	}
	raw, err := json.Marshal(payload)
	if err != nil { t.Fatalf("marshal webhook: %v", err) }

	send := func(signature string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/internal/v1/identity/idswyft-webhook", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Idswyft-Signature", signature)
		rec := httptest.NewRecorder()
		(&service{db: db}).idswyftIdentityWebhook(rec, req)
		return rec
	}

	first := send(idswyftWebhookSignature(raw, secret))
	if first.Code != http.StatusAccepted {
		t.Fatalf("first webhook status=%d body=%s", first.Code, first.Body.String())
	}
	second := send(idswyftWebhookSignature(raw, secret))
	if second.Code != http.StatusOK {
		t.Fatalf("replayed webhook status=%d body=%s", second.Code, second.Body.String())
	}
	if invalid := send("sha256=invalid"); invalid.Code != http.StatusUnauthorized {
		t.Fatalf("invalid signature status=%d body=%s", invalid.Code, invalid.Body.String())
	}

	var verified bool
	var result, provider, evidenceReference, requestID string
	var fingerprint []byte
	if err := db.QueryRow(ctx, `
		SELECT adult_verified_at IS NOT NULL, adult_verification_result,
		       adult_verification_provider, evidence_reference,
		       evidence_fingerprint_sha256, sensitive_action_request_id::text
		FROM testagram_identity.identity_applications WHERE id=$1::uuid
	`, applicationID).Scan(&verified, &result, &provider, &evidenceReference, &fingerprint, &requestID); err != nil {
		t.Fatalf("read verified application: %v", err)
	}
	expectedFingerprint := sha256.Sum256(raw)
	if !verified || result != "verified" || provider != "idswyft" ||
		evidenceReference != "idswyft:"+verificationID ||
		!bytes.Equal(fingerprint, expectedFingerprint[:]) || requestID == "" {
		t.Fatalf("unexpected verified application state: verified=%v result=%q provider=%q reference=%q request=%q", verified, result, provider, evidenceReference, requestID)
	}

	var actionType, requester, targetReference, status string
	var requiredApprovals int
	var reviewPayload []byte
	if err := db.QueryRow(ctx, `
		SELECT action_type, requested_by_appwrite_user_id, target_reference,
		       status, required_approvals, payload
		FROM testagram_auth.sensitive_action_requests WHERE id=$1::uuid
	`, requestID).Scan(&actionType, &requester, &targetReference, &status, &requiredApprovals, &reviewPayload); err != nil {
		t.Fatalf("read review request: %v", err)
	}
	if actionType != "identity.approve" || requester != "testagram-identity-service" ||
		targetReference != applicationID || status != "pending" || requiredApprovals != 2 {
		t.Fatalf("unexpected review request: action=%q requester=%q target=%q status=%q approvals=%d", actionType, requester, targetReference, status, requiredApprovals)
	}
	if strings.Contains(string(reviewPayload), "do-not-persist-raw-provider-payload") {
		t.Fatal("raw provider payload was persisted in the review payload")
	}
	var requestCount int
	if err := db.QueryRow(ctx, `
		SELECT count(*) FROM testagram_auth.sensitive_action_requests
		WHERE action_type='identity.approve' AND target_reference=$1
	`, applicationID).Scan(&requestCount); err != nil {
		t.Fatalf("count review requests: %v", err)
	}
	if requestCount != 1 {
		t.Fatalf("replayed webhook created %d review requests, want exactly one", requestCount)
	}
}
