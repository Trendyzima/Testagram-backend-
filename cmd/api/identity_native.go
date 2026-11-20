package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var identityApplicationIDPattern = regexp.MustCompile("^[0-9a-fA-F-]{36}$")
var identityIdempotencyKeyPattern = regexp.MustCompile("^[A-Za-z0-9_-]{43}$")

const (
	maxIdentityApplicationBody = 16 << 10
	maxIdentityEvidenceBytes   = int64(15 << 20)
	identityAdultThreshold     = 18
	nativeIdentitySessionTTL   = 24 * time.Hour
	identityEvidenceNamespace  = "identity-evidence-private"
)

type identityApplicationInput struct {
	Email           string `json:"email"`
	Username        string `json:"username"`
	DisplayName     string `json:"display_name"`
	AdultConfirmed  bool   `json:"adult_confirmed"`
	TermsAccepted   bool   `json:"terms_accepted"`
	PrivacyAccepted bool   `json:"privacy_accepted"`
}

type identityEvidenceUploadInput struct {
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
}

type identityEvidenceSubmitInput struct {
	EvidenceID string `json:"evidence_id"`
}

type identitySessionAccess struct {
	ApplicationID string
	Email         string
	Status        string
	Purpose       string
}

func identityDerivedCapabilityToken(idempotencyKey, purpose, resourceID string) string {
	mac := hmac.New(sha256.New, []byte(idempotencyKey))
	_, _ = mac.Write([]byte("testagram:identity:" + purpose + ":" + resourceID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func identityApplicationConflict(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	return pgErr.ConstraintName == "identity_applications_active_email_ci_idx" ||
		pgErr.ConstraintName == "identity_applications_active_username_ci_idx"
}

func identityApplicationNeedsVerificationSessionRenewal(status string, provisioningExists bool) bool {
	if provisioningExists {
		return false
	}
	return status == "pending" || status == "verification_pending"
}

func (s *service) identityApplicationCreate(w http.ResponseWriter, r *http.Request) {
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", requestID(r)))
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorBody("METHOD_NOT_ALLOWED", "POST required", requestID(r)))
		return
	}
	if !s.identityReviewWebAuthnReady {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_REVIEW_MFA_UNAVAILABLE", "Identity applications remain closed until staff WebAuthn step-up is implemented and verified", requestID(r)))
		return
	}
	if s.db == nil || !s.identityEvidenceStorageConfigured() {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_SERVICE_UNAVAILABLE", "Native verification requires PostgreSQL and the private Data Centre evidence lifecycle or a verified private storage replica", requestID(r)))
		return
	}
	storageCtx, storageCancel := context.WithTimeout(r.Context(), 3*time.Second)
	storageErr := s.identityEvidenceStorageAvailable(storageCtx)
	storageCancel()
	if storageErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORAGE_UNAVAILABLE", "Private identity evidence storage is not ready", requestID(r)))
		return
	}
	termsVersion := strings.TrimSpace(env("TESTAGRAM_TERMS_VERSION", ""))
	privacyVersion := strings.TrimSpace(env("TESTAGRAM_PRIVACY_VERSION", ""))
	if termsVersion == "" || len(termsVersion) > 64 || privacyVersion == "" || len(privacyVersion) > 64 {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("REGISTRATION_POLICY_NOT_CONFIGURED", "Registration policy versions are not configured", requestID(r)))
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if !identityIdempotencyKeyPattern.MatchString(idempotencyKey) {
		writeJSON(w, http.StatusBadRequest, errorBody("IDEMPOTENCY_KEY_REQUIRED", "A high-entropy Idempotency-Key is required", requestID(r)))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxIdentityApplicationBody)
	defer r.Body.Close()
	var input identityApplicationInput
	if err := decodeSingleJSON(r.Body, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_JSON", "Request body is invalid", requestID(r)))
		return
	}
	email, err := authEmail(input.Email)
	username := strings.ToLower(strings.TrimSpace(input.Username))
	displayName := strings.TrimSpace(input.DisplayName)
	if err != nil || !authUsernamePattern.MatchString(username) || len(displayName) < 2 || len(displayName) > 80 ||
		!input.AdultConfirmed || !input.TermsAccepted || !input.PrivacyAccepted {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_REGISTRATION_APPLICATION", "Provide valid account details and accept the age, Terms and Privacy declarations", requestID(r)))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	allowed, rateErr := s.authRateAllowed(ctx, "identity-application-ip", authClientIP(r), 5)
	if rateErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("RATE_LIMIT_UNAVAILABLE", "Registration is temporarily unavailable", requestID(r)))
		return
	}
	if !allowed {
		writeJSON(w, http.StatusTooManyRequests, errorBody("RATE_LIMITED", "Too many registration attempts. Try again later.", requestID(r)))
		return
	}
	submissionHash := authTokenHash(idempotencyKey)

	// Count only new application submissions against the per-email limit.
	// A retry with the same idempotency capability must remain recoverable even
	// after the original request consumed its rate-limit allowance.
	var preflightEmail, preflightUsername, preflightName string
	preflightErr := s.db.QueryRow(ctx, `
		SELECT email,username,display_name
		FROM testagram_identity.identity_applications
		WHERE submission_key_sha256=$1
	`, submissionHash).Scan(&preflightEmail, &preflightUsername, &preflightName)
	if preflightErr == nil {
		if preflightEmail != email || preflightUsername != username || preflightName != displayName {
			writeJSON(w, http.StatusConflict, errorBody("IDEMPOTENCY_KEY_REUSED", "Idempotency-Key was already used for a different application", requestID(r)))
			return
		}
	} else if errors.Is(preflightErr, pgx.ErrNoRows) {
		allowed, rateErr = s.authRateAllowed(ctx, "identity-application-email", email, 3)
		if rateErr != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("RATE_LIMIT_UNAVAILABLE", "Registration is temporarily unavailable", requestID(r)))
			return
		}
		if !allowed {
			writeJSON(w, http.StatusTooManyRequests, errorBody("RATE_LIMITED", "Too many registration attempts. Try again later.", requestID(r)))
			return
		}
	} else {
		log.Printf("identity application preflight lookup failed request_id=%s: %v", requestID(r), preflightErr)
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Application recovery state is unavailable", requestID(r)))
		return
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		log.Printf("identity application tx begin failed request_id=%s: %v", requestID(r), err)
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Registration is temporarily unavailable", requestID(r)))
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	// Serialize requests carrying the same idempotency capability across
	// API replicas. SELECT ... FOR UPDATE cannot lock a row that does not yet
	// exist, so without this transaction-scoped advisory lock two first-time
	// retries could race into the unique index and one caller would lose the
	// application capability.
	if _, err := tx.Exec(ctx, `
		SELECT pg_advisory_xact_lock(hashtextextended(encode($1::bytea,'hex'),0))
	`, submissionHash); err != nil {
		log.Printf("identity application advisory lock failed request_id=%s: %v", requestID(r), err)
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Registration is temporarily unavailable", requestID(r)))
		return
	}

	var applicationID, applicationStatus, oldEmail, oldUsername, oldName string
	err = tx.QueryRow(ctx, `
		SELECT id::text,status,email,username,display_name
		FROM testagram_identity.identity_applications
		WHERE submission_key_sha256=$1 FOR UPDATE
	`, submissionHash).Scan(&applicationID, &applicationStatus, &oldEmail, &oldUsername, &oldName)
	existing := err == nil
	if existing && (oldEmail != email || oldUsername != username || oldName != displayName) {
		writeJSON(w, http.StatusConflict, errorBody("IDEMPOTENCY_KEY_REUSED", "Idempotency-Key was already used for a different application", requestID(r)))
		return
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		log.Printf("identity application row lookup failed request_id=%s: %v", requestID(r), err)
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Registration is temporarily unavailable", requestID(r)))
		return
	}
	if !existing {
		if err := tx.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&applicationID); err != nil {
			log.Printf("identity application UUID generation failed request_id=%s: %v", requestID(r), err)
			writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Registration is temporarily unavailable", requestID(r)))
			return
		}
	}
	applicationToken := identityDerivedCapabilityToken(idempotencyKey, "application", applicationID)
	applicationTokenHash := authTokenHash(applicationToken)
	provisioningExists := false
	if existing {
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM testagram_identity.registration_provisioning
				WHERE application_id=$1::uuid
			)
		`, applicationID).Scan(&provisioningExists); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Application recovery state is unavailable", requestID(r)))
			return
		}
	}
	if existing && !identityApplicationNeedsVerificationSessionRenewal(applicationStatus, provisioningExists) {
		if _, err := tx.Exec(ctx, `
			UPDATE testagram_identity.identity_applications
			SET status_access_token_sha256=$2,status_access_token_expires_at=now()+interval '7 days',updated_at=now()
			WHERE id=$1::uuid
		`, applicationID, applicationTokenHash); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Application access could not be renewed", requestID(r)))
			return
		}
		if err := tx.Commit(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Application recovery could not be committed", requestID(r)))
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusAccepted, map[string]any{
			"ok": true, "application_id": applicationID, "application_token": applicationToken,
			"verification_session_id": "", "verification_session_token": "",
			"status": applicationStatus, "verification_method": "existing_application",
		})
		return
	}
	if !existing {
		_, err = tx.Exec(ctx, `
			INSERT INTO testagram_identity.identity_applications
			  (id,email,username,display_name,adult_confirmed_at,evidence_reference,
			   evidence_fingerprint_sha256,terms_version,privacy_version,terms_accepted_at,
			   privacy_accepted_at,submission_key_sha256,status_access_token_sha256,
			   status_access_token_expires_at,status)
			VALUES ($1::uuid,$2,$3,$4,now(),$5,NULL,$6,$7,now(),now(),$8,$9,
			        now()+interval '7 days','verification_pending')
		`, applicationID, email, username, displayName, "native-session-pending:"+applicationID,
			termsVersion, privacyVersion, submissionHash, applicationTokenHash)
		if err != nil {
			log.Printf("identity application insert failed request_id=%s: %v", requestID(r), err)
			if identityApplicationConflict(err) {
				// Duplicate email/username constraints receive the same neutral
				// acceptance as an ineligible application to prevent enumeration.
				writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "message": "If the application is eligible, the next verification step will be sent."})
				return
			}
			writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Registration is temporarily unavailable", requestID(r)))
			return
		}
		for _, consent := range []struct{ kind, version string }{{"terms", termsVersion}, {"privacy", privacyVersion}, {"age_confirmation", "age-v1"}} {
			if _, err := tx.Exec(ctx, `
				INSERT INTO testagram_identity.legal_consent_audit(application_id,consent_type,policy_version,accepted_at,request_id)
				VALUES ($1::uuid,$2,$3,now(),$4)
			`, applicationID, consent.kind, consent.version, requestID(r)); err != nil {
				log.Printf("identity consent audit insert failed request_id=%s: %v", requestID(r), err)
				writeJSON(w, http.StatusServiceUnavailable, errorBody("CONSENT_AUDIT_FAILED", "Registration consent could not be recorded", requestID(r)))
				return
			}
		}
	} else {
		if _, err := tx.Exec(ctx, `
			UPDATE testagram_identity.identity_applications
			SET status_access_token_sha256=$2,status_access_token_expires_at=now()+interval '7 days',updated_at=now()
			WHERE id=$1::uuid
		`, applicationID, applicationTokenHash); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Registration is temporarily unavailable", requestID(r)))
			return
		}
	}
	// Never reopen a session once evidence has been submitted or review has
	// started. This check includes older review sessions, not just the latest
	// row, so an expired later attempt cannot bypass a prior review.
	var reviewedSessionStatus string
	reviewErr := tx.QueryRow(ctx, `
		SELECT status
		FROM testagram_identity.verification_sessions
		WHERE application_id=$1::uuid AND purpose='age_assurance'
		  AND status IN ('submitted','manual_review','verified')
		ORDER BY created_at DESC LIMIT 1
		FOR UPDATE
	`, applicationID).Scan(&reviewedSessionStatus)
	if reviewErr == nil {
		if err := tx.Commit(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Application recovery could not be committed", requestID(r)))
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusAccepted, map[string]any{
			"ok": true, "application_id": applicationID, "application_token": applicationToken,
			"verification_session_id": "", "verification_session_token": "",
			"status": applicationStatus, "verification_method": "existing_application",
		})
		return
	}
	if !errors.Is(reviewErr, pgx.ErrNoRows) {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Application review state is unavailable", requestID(r)))
		return
	}

	// If the active session is still valid, reproduce the same bearer capability
	// from the high-entropy idempotency key and stable session ID. Repeated or
	// concurrent retries therefore cannot invalidate a token returned to a
	// sibling request. Only a missing/expired session is replaced.
	var sessionID string
	var expiresInSeconds int
	activeErr := tx.QueryRow(ctx, `
		SELECT id::text,
		       GREATEST(1, CEIL(EXTRACT(EPOCH FROM (expires_at-now()))))::int
		FROM testagram_identity.verification_sessions
		WHERE application_id=$1::uuid AND purpose='age_assurance'
		  AND status IN ('created','in_progress') AND expires_at>now()
		ORDER BY created_at DESC LIMIT 1
		FOR UPDATE
	`, applicationID).Scan(&sessionID, &expiresInSeconds)
	if activeErr == nil {
		sessionToken := identityDerivedCapabilityToken(idempotencyKey, "session", sessionID)
		sessionTokenHash := authTokenHash(sessionToken)
		if _, err := tx.Exec(ctx, `
			UPDATE testagram_identity.verification_sessions
			SET session_token_sha256=$2,updated_at=now()
			WHERE id=$1::uuid
		`, sessionID, sessionTokenHash); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Verification session access could not be renewed", requestID(r)))
			return
		}
		if err := tx.Commit(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Application recovery could not be committed", requestID(r)))
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusAccepted, map[string]any{
			"ok": true, "application_id": applicationID, "application_token": applicationToken,
			"verification_session_id": sessionID, "verification_session_token": sessionToken,
			"status": "verification_pending", "adult_verification_status": "pending",
			"verification_method": "manual_review", "expires_in_seconds": expiresInSeconds,
		})
		return
	}
	if !errors.Is(activeErr, pgx.ErrNoRows) {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Verification session state is unavailable", requestID(r)))
		return
	}

	// Expire stale open sessions before creating a replacement. Submitted and
	// manual-review sessions are deliberately excluded and can never be reset.
	if _, err := tx.Exec(ctx, `
		UPDATE testagram_identity.verification_sessions
		SET status='expired',updated_at=now()
		WHERE application_id=$1::uuid AND purpose='age_assurance'
		  AND status IN ('created','in_progress') AND expires_at<=now()
	`, applicationID); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Expired verification session could not be closed", requestID(r)))
		return
	}
	if err := tx.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&sessionID); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Verification session could not be created", requestID(r)))
		return
	}
	sessionToken := identityDerivedCapabilityToken(idempotencyKey, "session", sessionID)
	sessionTokenHash := authTokenHash(sessionToken)
	if _, err := tx.Exec(ctx, `
		INSERT INTO testagram_identity.verification_sessions
		  (id,application_id,session_token_sha256,purpose,requested_minimum_age,status,expires_at,assurance_method)
		VALUES ($1::uuid,$2::uuid,$3,'age_assurance',18,'in_progress',now()+interval '24 hours','manual_review')
	`, sessionID, applicationID, sessionTokenHash); err != nil {
		log.Printf("identity verification session insert failed request_id=%s: %v", requestID(r), err)
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Verification session could not be created", requestID(r)))
		return
	}
	for _, event := range []string{"session_created", "session_started"} {
		if _, err := tx.Exec(ctx, `
			INSERT INTO testagram_identity.verification_events(session_id,event_type,actor_type,actor_reference,request_id)
			VALUES ($1::uuid,$2,'applicant',$3,$4)
		`, sessionID, event, applicationID, requestID(r)); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_AUDIT_FAILED", "Verification session audit could not be recorded", requestID(r)))
			return
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE testagram_identity.identity_applications
		SET evidence_reference=$2,status='verification_pending',updated_at=now()
		WHERE id=$1::uuid
	`, applicationID, "native-session-pending:"+sessionID); err != nil {
		log.Printf("identity application session link failed request_id=%s: %v", requestID(r), err)
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Verification session could not be linked", requestID(r)))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		log.Printf("identity application create commit failed request_id=%s: %v", requestID(r), err)
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Registration is temporarily unavailable", requestID(r)))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok": true, "application_id": applicationID, "application_token": applicationToken,
		"verification_session_id": sessionID, "verification_session_token": sessionToken,
		"status": "verification_pending", "adult_verification_status": "pending",
		"verification_method": "manual_review", "expires_in_seconds": int(nativeIdentitySessionTTL.Seconds()),
	})
}

func (s *service) identityApplicationStatus(w http.ResponseWriter, r *http.Request) {
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", requestID(r)))
		return
	}
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorBody("METHOD_NOT_ALLOWED", "GET required", requestID(r)))
		return
	}
	applicationID := strings.TrimSpace(r.PathValue("application_id"))
	token := strings.TrimSpace(r.Header.Get("X-Application-Token"))
	if !identityApplicationIDPattern.MatchString(applicationID) || len(token) < 32 || len(token) > 128 || s.db == nil {
		writeJSON(w, http.StatusNotFound, errorBody("APPLICATION_NOT_FOUND", "Application was not found", requestID(r)))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	var status, verificationStatus, sessionID, provisioningStatus string
	var threshold int
	var updatedAt time.Time
	var storedHash []byte
	err := s.db.QueryRow(ctx, `
		SELECT a.status,COALESCE(v.status,'pending'),COALESCE(v.requested_minimum_age,18),a.updated_at,
		       a.status_access_token_sha256,COALESCE(v.id::text,''),COALESCE(nrp.status,'')
		FROM testagram_identity.identity_applications a
		LEFT JOIN LATERAL (
		  SELECT id,status,requested_minimum_age
		  FROM testagram_identity.verification_sessions
		  WHERE application_id=a.id AND purpose='age_assurance'
		  ORDER BY created_at DESC LIMIT 1
		) v ON true
		LEFT JOIN testagram_identity.native_registration_provisioning nrp ON nrp.application_id=a.id
		WHERE a.id=$1::uuid AND a.status_access_token_expires_at>now()
	`, applicationID).Scan(&status, &verificationStatus, &threshold, &updatedAt, &storedHash, &sessionID, &provisioningStatus)
	if err != nil || subtle.ConstantTimeCompare(storedHash, authTokenHash(token)) != 1 {
		writeJSON(w, http.StatusNotFound, errorBody("APPLICATION_NOT_FOUND", "Application was not found", requestID(r)))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"application_id": applicationID, "status": status,
		"adult_verification_status":    verificationStatus,
		"adult_verification_threshold": threshold,
		"verification_session_id":      sessionID,
		"account_provisioning_status":  provisioningStatus,
		"updated_at":                   updatedAt.UTC().Format(time.RFC3339),
	})
}

func (s *service) identitySessionAccess(r *http.Request) (identitySessionAccess, error) {
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	token := strings.TrimSpace(r.Header.Get("X-Verification-Session-Token"))
	if !identityApplicationIDPattern.MatchString(sessionID) || len(token) < 32 || len(token) > 128 || s.db == nil {
		return identitySessionAccess{}, pgx.ErrNoRows
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	var access identitySessionAccess
	var digest []byte
	err := s.db.QueryRow(ctx, `
		SELECT a.id::text,a.email,v.status,v.purpose,v.session_token_sha256
		FROM testagram_identity.verification_sessions v
		JOIN testagram_identity.identity_applications a ON a.id=v.application_id
		WHERE v.id=$1::uuid AND v.expires_at>now()
	`, sessionID).Scan(&access.ApplicationID, &access.Email, &access.Status, &access.Purpose, &digest)
	if err != nil || subtle.ConstantTimeCompare(digest, authTokenHash(token)) != 1 {
		return identitySessionAccess{}, pgx.ErrNoRows
	}
	return access, nil
}

func (s *service) identitySessionStatus(w http.ResponseWriter, r *http.Request) {
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", requestID(r)))
		return
	}
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorBody("METHOD_NOT_ALLOWED", "GET required", requestID(r)))
		return
	}
	access, err := s.identitySessionAccess(r)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorBody("SESSION_NOT_FOUND", "Verification session was not found", requestID(r)))
		return
	}
	var sessionID string
	var expiresAt, createdAt time.Time
	var submittedAt *time.Time
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	err = s.db.QueryRow(ctx, `
		SELECT id::text,created_at,expires_at,submitted_at
		FROM testagram_identity.verification_sessions WHERE id=$1::uuid
	`, r.PathValue("session_id")).Scan(&sessionID, &createdAt, &expiresAt, &submittedAt)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorBody("SESSION_NOT_FOUND", "Verification session was not found", requestID(r)))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": sessionID, "application_id": access.ApplicationID,
		"purpose": access.Purpose, "status": access.Status,
		"created_at":   createdAt.UTC().Format(time.RFC3339),
		"expires_at":   expiresAt.UTC().Format(time.RFC3339),
		"submitted_at": submittedAt,
	})
}

func identityEvidenceMatchesContentType(payload []byte, contentType string) bool {
	if len(payload) == 0 {
		return false
	}
	if contentType == "application/pdf" {
		return bytes.HasPrefix(payload, []byte("%PDF-"))
	}
	detected := strings.ToLower(strings.TrimSpace(strings.Split(http.DetectContentType(payload), ";")[0]))
	return detected == contentType
}

func identityEvidenceExtension(contentType string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(contentType)) {
	case "image/jpeg":
		return ".jpg", true
	case "image/png":
		return ".png", true
	case "image/webp":
		return ".webp", true
	case "application/pdf":
		return ".pdf", true
	default:
		return "", false
	}
}

func (s *service) identityEvidenceUploadURL(w http.ResponseWriter, r *http.Request) {
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", requestID(r)))
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorBody("METHOD_NOT_ALLOWED", "POST required", requestID(r)))
		return
	}
	access, err := s.identitySessionAccess(r)
	if err != nil || (access.Status != "in_progress" && access.Status != "created") {
		writeJSON(w, http.StatusNotFound, errorBody("SESSION_NOT_FOUND", "Verification session was not found", requestID(r)))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	defer r.Body.Close()
	var input identityEvidenceUploadInput
	if err := decodeSingleJSON(r.Body, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_JSON", "Evidence metadata is invalid", requestID(r)))
		return
	}
	extension, ok := identityEvidenceExtension(input.ContentType)
	digest, digestErr := hex.DecodeString(strings.TrimSpace(input.SHA256))
	if !ok || input.SizeBytes < 1 || input.SizeBytes > maxIdentityEvidenceBytes || digestErr != nil || len(digest) != 32 {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_EVIDENCE_METADATA", "Use a supported document image or PDF with a valid size and SHA-256 digest", requestID(r)))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	input.ContentType = strings.ToLower(strings.TrimSpace(input.ContentType))
	sessionID := r.PathValue("session_id")
	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Evidence upload could not be prepared", requestID(r)))
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	// Serialize all reservation attempts for this session across API replicas.
	// The partial unique index remains the final database-level invariant.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", sessionID); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Evidence upload could not be prepared", requestID(r)))
		return
	}

	var evidenceID, existingType string
	var existingSize int64
	var existingDigest []byte
	var reservationActive bool
	var expiresInSeconds int
	err = tx.QueryRow(ctx, `
		SELECT id::text,content_type,size_bytes,sha256,
		       created_at > now()-interval '5 minutes',
		       GREATEST(1,CEIL(EXTRACT(EPOCH FROM (created_at+interval '5 minutes'-now())))::int)
		FROM testagram_identity.verification_evidence
		WHERE session_id=$1::uuid AND status='upload_pending'
		FOR UPDATE
	`, sessionID).Scan(&evidenceID, &existingType, &existingSize, &existingDigest, &reservationActive, &expiresInSeconds)
	if err == nil && reservationActive {
		if existingType != input.ContentType || existingSize != input.SizeBytes ||
			len(existingDigest) != len(digest) || subtle.ConstantTimeCompare(existingDigest, digest) != 1 {
			writeJSON(w, http.StatusConflict, errorBody("EVIDENCE_ALREADY_RESERVED", "A different evidence upload is already reserved for this session", requestID(r)))
			return
		}
		if err := tx.Commit(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Evidence upload could not be prepared", requestID(r)))
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		uploadPath := "/api/identity/sessions/" + sessionID + "/evidence/" + evidenceID + "/upload"
		writeJSON(w, http.StatusCreated, map[string]any{
			"evidence_id": evidenceID, "upload_url": uploadPath,
			"method": http.MethodPut, "content_type": input.ContentType,
			"size_bytes": input.SizeBytes, "expires_in_seconds": expiresInSeconds,
		})
		return
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Evidence reservation could not be recovered", requestID(r)))
		return
	}
	if err == nil {
		// The five-minute upload capability has expired. Retire only the
		// unuploaded reservation so a fresh attempt can be made; submitted,
		// verified or quarantined evidence is never replaced here.
		if _, err := tx.Exec(ctx, `
			UPDATE testagram_identity.verification_evidence
			SET status='deleted',uploaded_at=now(),retention_until=now()
			WHERE id=$1::uuid AND status='upload_pending'
		`, evidenceID); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Expired evidence reservation could not be retired", requestID(r)))
			return
		}
	} else {
		var immutableEvidenceExists bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM testagram_identity.verification_evidence
				WHERE session_id=$1::uuid AND status IN ('verified','quarantined')
			)
		`, sessionID).Scan(&immutableEvidenceExists); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Evidence reservation state is unavailable", requestID(r)))
			return
		}
		if immutableEvidenceExists {
			writeJSON(w, http.StatusConflict, errorBody("EVIDENCE_ALREADY_RESERVED", "Evidence for this session has already been submitted or quarantined", requestID(r)))
			return
		}
	}

	if err := tx.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&evidenceID); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Evidence upload could not be prepared", requestID(r)))
		return
	}
	nonce, err := authRandomToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("TOKEN_FAILED", "Evidence upload could not be prepared", requestID(r)))
		return
	}
	objectKey := path.Join("identity", access.ApplicationID, sessionID, evidenceID+extension+"-"+nonce)
	if _, err := tx.Exec(ctx, `
		INSERT INTO testagram_identity.verification_evidence
		  (id,session_id,object_key,content_type,size_bytes,sha256)
		VALUES ($1::uuid,$2::uuid,$3,$4,$5,$6)
	`, evidenceID, sessionID, objectKey, input.ContentType, input.SizeBytes, digest); err != nil {
		writeJSON(w, http.StatusConflict, errorBody("EVIDENCE_ALREADY_RESERVED", "An evidence upload is already reserved for this session", requestID(r)))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Evidence upload could not be prepared", requestID(r)))
		return
	}
	expiresInSeconds = 300
	w.Header().Set("Cache-Control", "no-store")
	// Do not expose an internal MinIO hostname or a raw object-store capability
	// to mobile clients. The client uploads to this authenticated API route;
	// Backend streams the bounded payload to the private object store itself.
	uploadPath := "/api/identity/sessions/" + r.PathValue("session_id") + "/evidence/" + evidenceID + "/upload"
	writeJSON(w, http.StatusCreated, map[string]any{
		"evidence_id": evidenceID, "upload_url": uploadPath,
		"method": http.MethodPut, "content_type": input.ContentType,
		"size_bytes": input.SizeBytes, "expires_in_seconds": expiresInSeconds,
	})
}

func (s *service) identityEvidenceUpload(w http.ResponseWriter, r *http.Request) {
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", requestID(r)))
		return
	}
	if r.Method != http.MethodPut {
		writeJSON(w, http.StatusMethodNotAllowed, errorBody("METHOD_NOT_ALLOWED", "PUT required", requestID(r)))
		return
	}
	access, err := s.identitySessionAccess(r)
	if err != nil || (access.Status != "in_progress" && access.Status != "created") {
		writeJSON(w, http.StatusNotFound, errorBody("SESSION_NOT_FOUND", "Verification session was not found", requestID(r)))
		return
	}
	evidenceID := strings.TrimSpace(r.PathValue("evidence_id"))
	if !identityApplicationIDPattern.MatchString(evidenceID) || !s.identityEvidenceStorageConfigured() {
		writeJSON(w, http.StatusNotFound, errorBody("EVIDENCE_NOT_FOUND", "Evidence upload was not found", requestID(r)))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	var objectKey, contentType string
	var size int64
	var expectedDigest []byte
	err = s.db.QueryRow(ctx, `
		SELECT object_key,content_type,size_bytes,sha256
		FROM testagram_identity.verification_evidence
		WHERE id=$1::uuid AND session_id=$2::uuid AND status='upload_pending'
		  AND created_at > now()-interval '5 minutes'
	`, evidenceID, r.PathValue("session_id")).Scan(&objectKey, &contentType, &size, &expectedDigest)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorBody("EVIDENCE_NOT_FOUND", "Evidence upload was not found", requestID(r)))
		return
	}
	if r.ContentLength != size || strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type"))) != contentType {
		writeJSON(w, http.StatusBadRequest, errorBody("EVIDENCE_METADATA_MISMATCH", "Upload size and content type must match the reserved evidence metadata", requestID(r)))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxIdentityEvidenceBytes)
	defer r.Body.Close()
	payload, err := io.ReadAll(r.Body)
	if err != nil || int64(len(payload)) != size {
		writeJSON(w, http.StatusRequestEntityTooLarge, errorBody("EVIDENCE_SIZE_MISMATCH", "Evidence upload is incomplete or exceeds its reserved size", requestID(r)))
		return
	}
	actualDigest := sha256.Sum256(payload)
	if len(expectedDigest) != sha256.Size || subtle.ConstantTimeCompare(actualDigest[:], expectedDigest) != 1 {
		writeJSON(w, http.StatusConflict, errorBody("EVIDENCE_CHECKSUM_MISMATCH", "Evidence upload failed its SHA-256 integrity check", requestID(r)))
		return
	}
	if !identityEvidenceMatchesContentType(payload, contentType) {
		writeJSON(w, http.StatusBadRequest, errorBody("EVIDENCE_CONTENT_MISMATCH", "Evidence bytes do not match the declared content type", requestID(r)))
		return
	}
	uploadURL, err := s.identityEvidenceCreateUploadURL(ctx, access.ApplicationID, objectKey, contentType, size)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("PRIVATE_EVIDENCE_STORAGE_UNAVAILABLE", "Private evidence storage is unavailable", requestID(r)))
		return
	}
	upstreamRequest, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, bytes.NewReader(payload))
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("PRIVATE_EVIDENCE_STORAGE_UNAVAILABLE", "Evidence upload could not be forwarded", requestID(r)))
		return
	}
	upstreamRequest.Header.Set("Content-Type", contentType)
	upstreamRequest.ContentLength = size
	client := &http.Client{Timeout: 40 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	upstreamResponse, err := client.Do(upstreamRequest)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errorBody("PRIVATE_EVIDENCE_STORAGE_UNAVAILABLE", "Evidence could not be stored", requestID(r)))
		return
	}
	io.Copy(io.Discard, io.LimitReader(upstreamResponse.Body, 4096))
	upstreamResponse.Body.Close()
	if upstreamResponse.StatusCode < 200 || upstreamResponse.StatusCode >= 300 {
		writeJSON(w, http.StatusBadGateway, errorBody("PRIVATE_EVIDENCE_STORAGE_REJECTED", "Private storage rejected the evidence upload", requestID(r)))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (s *service) identityEvidenceSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", requestID(r)))
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorBody("METHOD_NOT_ALLOWED", "POST required", requestID(r)))
		return
	}
	access, err := s.identitySessionAccess(r)
	if err != nil || (access.Status != "in_progress" && access.Status != "created" && access.Status != "manual_review") {
		writeJSON(w, http.StatusNotFound, errorBody("SESSION_NOT_FOUND", "Verification session was not found", requestID(r)))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	defer r.Body.Close()
	var input identityEvidenceSubmitInput
	if err := decodeSingleJSON(r.Body, &input); err != nil || !identityApplicationIDPattern.MatchString(input.EvidenceID) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_EVIDENCE_REFERENCE", "Evidence reference is invalid", requestID(r)))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	// A client may lose the response after the transaction commits. Recognize
	// only the exact evidence item already linked to this session/application;
	// never accept a different upload after the session enters manual review.
	if access.Status == "manual_review" {
		var evidenceStatus, sessionEvidenceReference, applicationStatus string
		err := s.db.QueryRow(ctx, `
			SELECT e.status,v.evidence_reference,a.status
			FROM testagram_identity.verification_evidence e
			JOIN testagram_identity.verification_sessions v ON v.id=e.session_id
			JOIN testagram_identity.identity_applications a ON a.id=v.application_id
			WHERE e.id=$1::uuid AND e.session_id=$2::uuid
			  AND v.status='manual_review'
		`, input.EvidenceID, r.PathValue("session_id")).Scan(&evidenceStatus, &sessionEvidenceReference, &applicationStatus)
		if err == nil && evidenceStatus == "verified" &&
			sessionEvidenceReference == "identity-evidence:"+input.EvidenceID &&
			applicationStatus == "under_review" {
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, http.StatusAccepted, map[string]any{
				"ok": true, "status": "manual_review", "idempotent_replay": true,
				"message": "Evidence was securely received and queued for manual review.",
			})
			return
		}
		writeJSON(w, http.StatusConflict, errorBody("SESSION_NOT_SUBMITTABLE", "Verification session is no longer accepting evidence", requestID(r)))
		return
	}
	if !s.identityEvidenceStorageConfigured() {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("PRIVATE_EVIDENCE_STORAGE_UNAVAILABLE", "Private evidence storage and verified replication are not ready", requestID(r)))
		return
	}
	var objectKey, contentType string
	var size int64
	var digest []byte
	err = s.db.QueryRow(ctx, `
		SELECT object_key,content_type,size_bytes,sha256
		FROM testagram_identity.verification_evidence
		WHERE id=$1::uuid AND session_id=$2::uuid AND status='upload_pending'
		  AND created_at > now()-interval '5 minutes'
	`, input.EvidenceID, r.PathValue("session_id")).Scan(&objectKey, &contentType, &size, &digest)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorBody("EVIDENCE_NOT_FOUND", "Evidence upload was not found", requestID(r)))
		return
	}
	if err := s.identityEvidenceVerifyAndReplicate(ctx, access.ApplicationID, objectKey, contentType, size, hex.EncodeToString(digest)); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("EVIDENCE_REPLICATION_INCOMPLETE", "Evidence was received but could not be verified across all private storage providers. Retry submission.", requestID(r)))
		return
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Evidence submission could not be recorded", requestID(r)))
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var currentStatus string
	if err := tx.QueryRow(ctx, `
		SELECT status FROM testagram_identity.verification_sessions
		WHERE id=$1::uuid AND application_id=$2::uuid AND expires_at>now()
		FOR UPDATE
	`, r.PathValue("session_id"), access.ApplicationID).Scan(&currentStatus); err != nil || (currentStatus != "in_progress" && currentStatus != "created") {
		writeJSON(w, http.StatusConflict, errorBody("SESSION_NOT_SUBMITTABLE", "Verification session is no longer accepting evidence", requestID(r)))
		return
	}
	if _, err := tx.Exec(ctx, `
		UPDATE testagram_identity.verification_evidence
		SET status='verified',uploaded_at=now(),verified_at=now()
		WHERE id=$1::uuid AND status='upload_pending'
	`, input.EvidenceID); err != nil {
		writeJSON(w, http.StatusConflict, errorBody("EVIDENCE_ALREADY_SUBMITTED", "Evidence has already been submitted", requestID(r)))
		return
	}
	evidenceRef := "identity-evidence:" + input.EvidenceID
	if _, err := tx.Exec(ctx, `
		UPDATE testagram_identity.verification_sessions
		SET status='manual_review',evidence_reference=$2,evidence_fingerprint_sha256=$3,
		    submitted_at=now(),updated_at=now()
		WHERE id=$1::uuid
	`, r.PathValue("session_id"), evidenceRef, digest); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Evidence submission could not be committed", requestID(r)))
		return
	}
	if _, err := tx.Exec(ctx, `
		UPDATE testagram_identity.identity_applications
		SET evidence_reference=$2,evidence_fingerprint_sha256=$3,
		    status='under_review',updated_at=now()
		WHERE id=$1::uuid
	`, access.ApplicationID, evidenceRef, digest); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Evidence submission could not be committed", requestID(r)))
		return
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO testagram_identity.verification_events(session_id,event_type,actor_type,actor_reference,request_id,metadata)
		VALUES ($1::uuid,'evidence_submitted','applicant',$2,$3,jsonb_build_object('evidence_id',$4::text))
	`, r.PathValue("session_id"), access.ApplicationID, requestID(r), input.EvidenceID); err != nil {
		log.Printf("identity evidence audit insert failed request_id=%s: %v", requestID(r), err)
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_AUDIT_FAILED", "Evidence submission audit could not be recorded", requestID(r)))
		return
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO testagram_identity.verification_events(session_id,event_type,actor_type,actor_reference,request_id)
		VALUES ($1::uuid,'review_requested','system','testagram-identity-service',$2)
	`, r.PathValue("session_id"), requestID(r)); err != nil {
		log.Printf("identity review-request audit insert failed request_id=%s: %v", requestID(r), err)
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_AUDIT_FAILED", "Review request audit could not be recorded", requestID(r)))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Evidence submission could not be committed", requestID(r)))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "status": "manual_review", "message": "Evidence was securely received and queued for manual review."})
}
