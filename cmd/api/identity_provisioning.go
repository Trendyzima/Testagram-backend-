package main

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *service) identityApplicationToken(r *http.Request) (string, error) {
	id := strings.TrimSpace(r.PathValue("application_id"))
	token := strings.TrimSpace(r.Header.Get("X-Application-Token"))
	if !identityApplicationIDPattern.MatchString(id) || len(token) < 32 || len(token) > 128 || s.db == nil {
		return "", pgx.ErrNoRows
	}

	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()

	var digest []byte
	err := s.db.QueryRow(ctx, `SELECT status_access_token_sha256
		FROM testagram_identity.identity_applications
		WHERE id=$1::uuid AND status_access_token_expires_at>now()`, id).Scan(&digest)
	if err != nil || subtle.ConstantTimeCompare(digest, authTokenHash(token)) != 1 {
		return "", pgx.ErrNoRows
	}
	return id, nil
}

func (s *service) identityProvisioningToken(w http.ResponseWriter, r *http.Request) {
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", requestID(r)))
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorBody("METHOD_NOT_ALLOWED", "POST required", requestID(r)))
		return
	}

	applicationID, err := s.identityApplicationToken(r)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorBody("APPLICATION_NOT_FOUND", "Application was not found", requestID(r)))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	allowed, rateErr := s.authRateAllowed(ctx, "identity-provisioning-token-ip", authClientIP(r), 5)
	if rateErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("RATE_LIMIT_UNAVAILABLE", "Provisioning authorization is temporarily unavailable", requestID(r)))
		return
	}
	if !allowed {
		writeJSON(w, http.StatusTooManyRequests, errorBody("RATE_LIMITED", "Too many provisioning authorization requests. Try again later.", requestID(r)))
		return
	}
	token, err := authRandomToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("TOKEN_FAILED", "Provisioning authorization could not be issued", requestID(r)))
		return
	}
	jti, err := authRandomToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("TOKEN_FAILED", "Provisioning authorization could not be issued", requestID(r)))
		return
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Provisioning authorization is unavailable", requestID(r)))
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var status, linkedRequestID, email, username, displayName, termsVersion, privacyVersion string
	var evidenceFingerprint []byte
	var adultVerified, termsAccepted, privacyAccepted bool
	err = tx.QueryRow(ctx, `
		SELECT status,COALESCE(native_sensitive_action_request_id::text,''),email,username,display_name,
		       evidence_fingerprint_sha256,terms_version,privacy_version,
		       adult_verified_at IS NOT NULL,terms_accepted_at IS NOT NULL,privacy_accepted_at IS NOT NULL
		FROM testagram_identity.identity_applications
		WHERE id=$1::uuid AND status_access_token_expires_at>now()
		FOR UPDATE
	`, applicationID).Scan(&status,&linkedRequestID,&email,&username,&displayName,
		&evidenceFingerprint,&termsVersion,&privacyVersion,&adultVerified,&termsAccepted,&privacyAccepted)
	if err != nil || status != "approved" || linkedRequestID == "" ||
		!adultVerified || !termsAccepted || !privacyAccepted || len(evidenceFingerprint) != 32 {
		writeJSON(w, http.StatusConflict, errorBody("APPLICATION_NOT_APPROVED", "Application has not completed both approval stages", requestID(r)))
		return
	}

	var firstApprover, secondApprover string
	var consumed bool
	err = tx.QueryRow(ctx, `
		SELECT COALESCE(first_approver_user_id::text,''),COALESCE(second_approver_user_id::text,''),consumed_at IS NOT NULL
		FROM testagram_identity.identity_approval_tokens
		WHERE application_id=$1::uuid
		FOR UPDATE
	`, applicationID).Scan(&firstApprover,&secondApprover,&consumed)
	if err != nil || firstApprover == "" || secondApprover == "" ||
		firstApprover == secondApprover || consumed {
		writeJSON(w, http.StatusConflict, errorBody("PROVISIONING_AUTHORIZATION_UNAVAILABLE", "Provisioning authorization is already consumed or unavailable", requestID(r)))
		return
	}

	var approvalCircuitValid bool
	err = tx.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1
		  FROM testagram_auth.native_sensitive_action_requests sar
		  WHERE sar.id=$1::uuid AND sar.action_type='identity.approve'
		    AND sar.status='approved' AND sar.required_approvals=2
		    AND sar.target_reference=$2 AND sar.requested_by_user_id IS NULL
		    AND sar.requested_by_service='identity_pipeline' AND sar.expires_at>now()
		    AND sar.payload->>'application_id'=$2
		    AND sar.payload->>'email'=$3
		    AND sar.payload->>'username'=$4
		    AND sar.payload->>'display_name'=$5
		    AND sar.payload->>'evidence_fingerprint_sha256'=$6
		    AND sar.payload->>'terms_version'=$7
		    AND sar.payload->>'privacy_version'=$8
		    AND (
		      SELECT count(DISTINCT approver_user_id)
		      FROM testagram_auth.native_sensitive_action_approvals
		      WHERE request_id=sar.id AND decision='approved'
		    )=2
		    AND (
		      SELECT count(DISTINCT approver_user_id)
		      FROM testagram_auth.native_sensitive_action_approvals
		      WHERE request_id=sar.id AND decision='approved'
		        AND approver_user_id IN ($9::uuid,$10::uuid)
		    )=2
		    AND (
		      SELECT count(DISTINCT reviewer_user_id)
		      FROM testagram_identity.identity_application_reviews
		      WHERE application_id=$2::uuid AND review_stage='evidence_review'
		        AND decision='approved'
		        AND evidence_audit_reference=sar.payload->>'evidence_audit_reference'
		    )=2
		    AND (
		      SELECT count(DISTINCT reviewer_user_id)
		      FROM testagram_identity.identity_application_reviews
		      WHERE application_id=$2::uuid AND review_stage='account_approval'
		        AND decision='approved'
		        AND reviewer_user_id IN ($9::uuid,$10::uuid)
		    )=2
		    AND NOT EXISTS (
		      SELECT 1 FROM testagram_identity.identity_application_reviews
		      WHERE application_id=$2::uuid AND review_stage='evidence_review'
		        AND decision='approved'
		        AND reviewer_user_id IN ($9::uuid,$10::uuid)
		    )
		)
	`, linkedRequestID,applicationID,email,username,displayName,
		hex.EncodeToString(evidenceFingerprint),termsVersion,privacyVersion,
		firstApprover,secondApprover).Scan(&approvalCircuitValid)
	if err != nil || !approvalCircuitValid {
		writeJSON(w, http.StatusConflict, errorBody("APPROVAL_LEDGER_INVALID", "Two independent evidence reviewers and two independent account approvers are required", requestID(r)))
		return
	}

	tag, err := tx.Exec(ctx, `UPDATE testagram_identity.identity_approval_tokens
		SET token_sha256=$2,jti_sha256=$3,issued_at=now(),expires_at=now()+interval '15 minutes'
		WHERE application_id=$1::uuid AND consumed_at IS NULL`,
		applicationID, authTokenHash(token), authTokenHash(jti))
	if err != nil || tag.RowsAffected() != 1 {
		writeJSON(w, http.StatusConflict, errorBody("PROVISIONING_AUTHORIZATION_UNAVAILABLE", "Provisioning authorization is already consumed or unavailable", requestID(r)))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Provisioning authorization could not be committed", requestID(r)))
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "approved", "approval_token": token, "jti": jti, "expires_in_seconds": 900,
	})
}
