package main

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type nativeIdentityRedeemInput struct {
	Password string `json:"password"`
}
type nativeIdentityVerifyInput struct {
	Token string `json:"token"`
}

func (s *service) identityProvisioningRedeemNative(w http.ResponseWriter, r *http.Request) {
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", requestID(r)))
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorBody("METHOD_NOT_ALLOWED", "POST required", requestID(r)))
		return
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Account provisioning is unavailable", requestID(r)))
		return
	}

	applicationID := strings.TrimSpace(r.PathValue("application_id"))
	applicationToken := strings.TrimSpace(r.Header.Get("X-Application-Token"))
	approvalToken := strings.TrimSpace(r.Header.Get("X-Identity-Approval-Token"))
	jti := strings.TrimSpace(r.Header.Get("X-Identity-Approval-JTI"))
	if !identityApplicationIDPattern.MatchString(applicationID) ||
		len(applicationToken) < 32 || len(applicationToken) > 128 ||
		len(approvalToken) < 32 || len(approvalToken) > 128 ||
		len(jti) < 32 || len(jti) > 128 {
		writeJSON(w, http.StatusNotFound, errorBody("PROVISIONING_AUTHORIZATION_INVALID", "Provisioning authorization was not found", requestID(r)))
		return
	}

	preflightID, preflightErr := s.identityApplicationToken(r)
	if preflightErr != nil || preflightID != applicationID {
		writeJSON(w, http.StatusNotFound, errorBody("APPLICATION_NOT_FOUND", "Application was not found", requestID(r)))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	allowed, rateErr := s.authRateAllowed(ctx, "identity-provisioning-ip", authClientIP(r), 8)
	if rateErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("RATE_LIMIT_UNAVAILABLE", "Account provisioning is temporarily unavailable", requestID(r)))
		return
	}
	if !allowed {
		writeJSON(w, http.StatusTooManyRequests, errorBody("RATE_LIMITED", "Too many provisioning attempts. Try again later.", requestID(r)))
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	defer r.Body.Close()
	var input nativeIdentityRedeemInput
	if err := decodeSingleJSON(r.Body, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_JSON", "Request body is invalid", requestID(r)))
		return
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Account provisioning is unavailable", requestID(r)))
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var email, username, displayName, applicationStatus, approvalRequestID, termsVersion, privacyVersion string
	var evidenceFingerprint, storedApplicationToken []byte
	err = tx.QueryRow(ctx, `
		SELECT email,username,display_name,status,
		       COALESCE(native_sensitive_action_request_id::text,''),
		       evidence_fingerprint_sha256,terms_version,privacy_version,status_access_token_sha256
		FROM testagram_identity.identity_applications
		WHERE id=$1::uuid AND status_access_token_expires_at>now()
		FOR UPDATE
	`, applicationID).Scan(&email, &username, &displayName, &applicationStatus, &approvalRequestID,
		&evidenceFingerprint, &termsVersion, &privacyVersion, &storedApplicationToken)
	if err != nil || subtle.ConstantTimeCompare(storedApplicationToken, authTokenHash(applicationToken)) != 1 {
		writeJSON(w, http.StatusNotFound, errorBody("APPLICATION_NOT_FOUND", "Application was not found", requestID(r)))
		return
	}
	if len(evidenceFingerprint) != 32 || termsVersion == "" || privacyVersion == "" {
		writeJSON(w, http.StatusConflict, errorBody("APPLICATION_EVIDENCE_INCOMPLETE", "Identity evidence and consent records are incomplete", requestID(r)))
		return
	}
	if applicationStatus != "approved" && applicationStatus != "verification_pending" && applicationStatus != "verified" {
		writeJSON(w, http.StatusConflict, errorBody("APPLICATION_NOT_APPROVED", "Application has not completed both approval stages", requestID(r)))
		return
	}

	var storedApprovalHash, storedJTIHash []byte
	var firstApprover, secondApprover string
	var tokenExpires time.Time
	var consumedAt *time.Time
	var consumedByNativeUserID string
	err = tx.QueryRow(ctx, `
		SELECT token_sha256,jti_sha256,first_approver_user_id::text,
		       second_approver_user_id::text,expires_at,consumed_at,
		       COALESCE(consumed_by_native_user_id::text,'')
		FROM testagram_identity.identity_approval_tokens
		WHERE application_id=$1::uuid
		FOR UPDATE
	`, applicationID).Scan(&storedApprovalHash, &storedJTIHash, &firstApprover, &secondApprover,
		&tokenExpires, &consumedAt, &consumedByNativeUserID)
	if err != nil ||
		subtle.ConstantTimeCompare(storedApprovalHash, authTokenHash(approvalToken)) != 1 ||
		subtle.ConstantTimeCompare(storedJTIHash, authTokenHash(jti)) != 1 ||
		firstApprover == "" || secondApprover == "" || firstApprover == secondApprover {
		writeJSON(w, http.StatusConflict, errorBody("PROVISIONING_AUTHORIZATION_INVALID", "Provisioning authorization is invalid or expired", requestID(r)))
		return
	}

	if consumedAt != nil {
		var userID, provisioningStatus string
		replayErr := tx.QueryRow(ctx, `
			SELECT p.user_id::text,p.status
			FROM testagram_identity.native_registration_provisioning p
			WHERE p.application_id=$1::uuid AND p.user_id=$2::uuid
		`, applicationID, consumedByNativeUserID).Scan(&userID, &provisioningStatus)
		if replayErr != nil || consumedByNativeUserID == "" {
			writeJSON(w, http.StatusConflict, errorBody("PROVISIONING_AUTHORIZATION_REPLAYED", "Provisioning authorization has already been consumed", requestID(r)))
			return
		}
		if err := tx.Commit(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Provisioning result could not be confirmed", requestID(r)))
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "provisioned", "canonical_user_id": userID,
			"email_verification_required": provisioningStatus == "verification_pending",
			"idempotent_replay":           true,
		})
		return
	}
	if !tokenExpires.After(time.Now()) || applicationStatus != "approved" || approvalRequestID == "" {
		writeJSON(w, http.StatusConflict, errorBody("PROVISIONING_AUTHORIZATION_EXPIRED", "Provisioning authorization is invalid or expired", requestID(r)))
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
	`, approvalRequestID,applicationID,email,username,displayName,
		hex.EncodeToString(evidenceFingerprint),termsVersion,privacyVersion,
		firstApprover,secondApprover).Scan(&approvalCircuitValid)
	if err != nil || !approvalCircuitValid {
		writeJSON(w, http.StatusConflict, errorBody("APPROVAL_LEDGER_INVALID", "Two independent evidence reviewers and two independent account approvers are required", requestID(r)))
		return
	}

	// Perform the expensive password KDF only after the application-scoped
	// capability and both independent approval ledgers have been validated.
	passwordHash, err := authPasswordHash(input.Password)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_PASSWORD", "Password must be 12–128 bytes", requestID(r)))
		return
	}
	verificationToken, err := authRandomToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("TOKEN_FAILED", "Email verification could not be initialized", requestID(r)))
		return
	}

	var userID string
	err = tx.QueryRow(ctx, `
		INSERT INTO testagram_auth.identities
		  (email,username,password_hash,verification_token_hash,verification_expires_at)
		VALUES ($1,$2,$3,$4,now()+interval '24 hours')
		RETURNING user_id::text
	`, email, username, passwordHash, authTokenHash(verificationToken)).Scan(&userID)
	if err != nil {
		writeJSON(w, http.StatusConflict, errorBody("ACCOUNT_ALREADY_EXISTS", "This approved application cannot create a duplicate Testagram account", requestID(r)))
		return
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO testagram_local.accounts(user_id,username,display_name)
		VALUES($1::uuid,$2,$3)
	`, userID, username, displayName); err != nil {
		writeJSON(w, http.StatusConflict, errorBody("PROFILE_PROVISIONING_FAILED", "The approved account profile could not be created", requestID(r)))
		return
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO testagram_identity.native_registration_provisioning(application_id,user_id,status)
		VALUES($1::uuid,$2::uuid,'verification_pending')
	`, applicationID, userID); err != nil {
		writeJSON(w, http.StatusConflict, errorBody("PROVISIONING_LEDGER_FAILED", "The approved account could not be linked to its application", requestID(r)))
		return
	}
	tag, err := tx.Exec(ctx, `
		UPDATE testagram_identity.identity_approval_tokens
		SET consumed_at=now(),consumed_by_native_user_id=$2::uuid
		WHERE application_id=$1::uuid AND consumed_at IS NULL
		  AND token_sha256=$3 AND jti_sha256=$4 AND expires_at>now()
	`, applicationID, userID, authTokenHash(approvalToken), authTokenHash(jti))
	if err != nil || tag.RowsAffected() != 1 {
		writeJSON(w, http.StatusConflict, errorBody("PROVISIONING_AUTHORIZATION_REPLAYED", "Provisioning authorization could not be consumed", requestID(r)))
		return
	}
	tag, err = tx.Exec(ctx, `
		UPDATE testagram_identity.identity_applications
		SET status='verification_pending',updated_at=now()
		WHERE id=$1::uuid AND status='approved'
	`, applicationID)
	if err != nil || tag.RowsAffected() != 1 {
		writeJSON(w, http.StatusConflict, errorBody("APPLICATION_STATE_CHANGED", "Application changed before provisioning could be committed", requestID(r)))
		return
	}
	var verificationSessionID string
	if err := tx.QueryRow(ctx, `
		SELECT id::text FROM testagram_identity.verification_sessions
		WHERE application_id=$1::uuid AND purpose='age_assurance' AND status='verified'
		ORDER BY decided_at DESC LIMIT 1
		FOR UPDATE
	`, applicationID).Scan(&verificationSessionID); err != nil {
		writeJSON(w, http.StatusConflict, errorBody("VERIFIED_EVIDENCE_SESSION_REQUIRED", "The approved application has no completed evidence-review session", requestID(r)))
		return
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO testagram_identity.verification_events(session_id,event_type,actor_type,actor_reference,request_id,metadata)
		VALUES($1::uuid,'account_provisioned','system','testagram-native-provisioning',$2,
		       jsonb_build_object('native_user_id',$3::text,'application_id',$4::text))
	`, verificationSessionID, requestID(r), userID, applicationID); err != nil {
		log.Printf("native identity provisioning audit insert failed request_id=%s: %v", requestID(r), err)
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_AUDIT_FAILED", "Provisioning audit could not be committed", requestID(r)))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("PROVISIONING_OUTCOME_UNCERTAIN", "Provisioning outcome is being reconciled; retry with the same approval authorization", requestID(r)))
		return
	}

	base := strings.TrimRight(strings.TrimSpace(os.Getenv("AUTH_PUBLIC_BASE_URL")), "/")
	link := base + "/verify-email?application_id=" + url.QueryEscape(applicationID) + "&token=" + url.QueryEscape(verificationToken)
	mailErr := s.sendAuthEmail(ctx, email, "Verify your Testagram account",
		"<p>Your identity application has been approved.</p><p><a href=\""+link+"\">Verify your Testagram email</a></p><p>This link expires in 24 hours and can only be used once.</p>")
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status": "provisioned", "canonical_user_id": userID,
		"email_verification_required": true, "email_delivery_pending": mailErr != nil,
	})
}

func (s *service) identityProvisioningVerifyEmail(w http.ResponseWriter, r *http.Request) {
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", requestID(r)))
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorBody("METHOD_NOT_ALLOWED", "POST required", requestID(r)))
		return
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Email verification is unavailable", requestID(r)))
		return
	}
	applicationID := strings.TrimSpace(r.PathValue("application_id"))
	if !identityApplicationIDPattern.MatchString(applicationID) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_APPLICATION", "Application reference is invalid", requestID(r)))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	defer r.Body.Close()
	var input nativeIdentityVerifyInput
	if err := decodeSingleJSON(r.Body, &input); err != nil || len(input.Token) < 32 || len(input.Token) > 128 {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_TOKEN", "Verification token is invalid or expired", requestID(r)))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	allowed, rateErr := s.authRateAllowed(ctx, "identity-email-verification-ip", authClientIP(r), 20)
	if rateErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("RATE_LIMIT_UNAVAILABLE", "Email verification is temporarily unavailable", requestID(r)))
		return
	}
	if !allowed {
		writeJSON(w, http.StatusTooManyRequests, errorBody("RATE_LIMITED", "Too many email verification attempts. Try again later.", requestID(r)))
		return
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Email verification is unavailable", requestID(r)))
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var userID string
	err = tx.QueryRow(ctx, `
		SELECT i.user_id::text
		FROM testagram_identity.native_registration_provisioning p
		JOIN testagram_identity.identity_applications a ON a.id=p.application_id
		JOIN testagram_auth.identities i ON i.user_id=p.user_id
		WHERE p.application_id=$1::uuid AND p.status='verification_pending'
		  AND a.status='verification_pending'
		  AND i.verification_token_hash=$2 AND i.verification_expires_at>now()
		  AND i.email_verified_at IS NULL AND i.disabled_at IS NULL
		FOR UPDATE OF p,i,a
	`, applicationID, authTokenHash(input.Token)).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_TOKEN", "Verification token is invalid or expired", requestID(r)))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Email verification could not be checked", requestID(r)))
		return
	}
	tag, err := tx.Exec(ctx, `
		UPDATE testagram_auth.identities
		SET email_verified_at=now(),verification_token_hash=NULL,
		    verification_expires_at=NULL,updated_at=now()
		WHERE user_id=$1::uuid AND email_verified_at IS NULL
	`, userID)
	if err != nil || tag.RowsAffected() != 1 {
		writeJSON(w, http.StatusConflict, errorBody("VERIFICATION_STATE_CHANGED", "Email verification state changed; request a new link if needed", requestID(r)))
		return
	}
	tag, err = tx.Exec(ctx, `
		UPDATE testagram_identity.native_registration_provisioning
		SET status='active',verified_at=now(),updated_at=now()
		WHERE application_id=$1::uuid AND user_id=$2::uuid AND status='verification_pending'
	`, applicationID, userID)
	if err != nil || tag.RowsAffected() != 1 {
		writeJSON(w, http.StatusConflict, errorBody("PROVISIONING_STATE_CHANGED", "Account activation could not be committed", requestID(r)))
		return
	}
	var verificationSessionID string
	if err := tx.QueryRow(ctx, `
		SELECT id::text FROM testagram_identity.verification_sessions
		WHERE application_id=$1::uuid AND purpose='age_assurance'
		ORDER BY created_at DESC LIMIT 1
		FOR UPDATE
	`, applicationID).Scan(&verificationSessionID); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_AUDIT_FAILED", "Verification audit session is unavailable", requestID(r)))
		return
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO testagram_identity.verification_events(session_id,event_type,actor_type,actor_reference,request_id,metadata)
		VALUES($1::uuid,'email_verified','applicant',$2,$3,jsonb_build_object('native_user_id',$4::text))
	`, verificationSessionID, userID, requestID(r), userID); err != nil {
		log.Printf("native email verification audit insert failed request_id=%s: %v", requestID(r), err)
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_AUDIT_FAILED", "Email verification audit could not be committed", requestID(r)))
		return
	}
	tag, err = tx.Exec(ctx, `
		UPDATE testagram_identity.identity_applications
		SET status='verified',updated_at=now()
		WHERE id=$1::uuid AND status='verification_pending'
	`, applicationID)
	if err != nil || tag.RowsAffected() != 1 {
		writeJSON(w, http.StatusConflict, errorBody("APPLICATION_STATE_CHANGED", "Approved application state changed before activation", requestID(r)))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Email verification could not be committed", requestID(r)))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "active", "email_verified": true, "canonical_user_id": userID,
	})
}

func (s *service) identityProvisioningResendVerification(w http.ResponseWriter, r *http.Request) {
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", requestID(r)))
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorBody("METHOD_NOT_ALLOWED", "POST required", requestID(r)))
		return
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Verification email is unavailable", requestID(r)))
		return
	}
	applicationID := strings.TrimSpace(r.PathValue("application_id"))
	applicationToken := strings.TrimSpace(r.Header.Get("X-Application-Token"))
	if !identityApplicationIDPattern.MatchString(applicationID) || len(applicationToken) < 32 || len(applicationToken) > 128 {
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "message": "If the application is awaiting email verification, a new link will be sent."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	allowed, rateErr := s.authRateAllowed(ctx, "identity-verification-resend-ip", authClientIP(r), 5)
	if rateErr != nil || !allowed {
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "message": "If the application is awaiting email verification, a new link will be sent."})
		return
	}
	token, err := authRandomToken()
	if err != nil {
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "message": "If the application is awaiting email verification, a new link will be sent."})
		return
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Verification email could not be requested", requestID(r)))
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var email string
	var storedApplicationToken []byte
	err = tx.QueryRow(ctx, `
		SELECT i.email,a.status_access_token_sha256
		FROM testagram_identity.native_registration_provisioning p
		JOIN testagram_identity.identity_applications a ON a.id=p.application_id
		JOIN testagram_auth.identities i ON i.user_id=p.user_id
		WHERE p.application_id=$1::uuid AND p.status='verification_pending'
		  AND a.status='verification_pending' AND i.email_verified_at IS NULL
		  AND a.status_access_token_expires_at>now()
		FOR UPDATE OF p,a,i
	`, applicationID).Scan(&email, &storedApplicationToken)
	if err != nil || subtle.ConstantTimeCompare(storedApplicationToken, authTokenHash(applicationToken)) != 1 {
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "message": "If the application is awaiting email verification, a new link will be sent."})
		return
	}
	if _, err := tx.Exec(ctx, `
		UPDATE testagram_auth.identities
		SET verification_token_hash=$2,verification_expires_at=now()+interval '24 hours',updated_at=now()
		WHERE email_normalized=$1 AND email_verified_at IS NULL
	`, email, authTokenHash(token)); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Verification email could not be requested", requestID(r)))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Verification email could not be requested", requestID(r)))
		return
	}
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("AUTH_PUBLIC_BASE_URL")), "/")
	link := base + "/verify-email?application_id=" + url.QueryEscape(applicationID) + "&token=" + url.QueryEscape(token)
	mailErr := s.sendAuthEmail(ctx, email, "Verify your Testagram account",
		"<p>Verify your Testagram account:</p><p><a href=\""+link+"\">Verify email</a></p><p>This link expires in 24 hours and can only be used once.</p>")
	if mailErr != nil {
		log.Printf("native identity verification resend delivery failed request_id=%s: %v", requestID(r), mailErr)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok":                     true,
		"email_delivery_pending": mailErr != nil,
		"message":                "If the application is awaiting email verification, a new link will be sent.",
	})
}
