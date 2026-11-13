package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var identityApplicationIDPattern = regexp.MustCompile("^[0-9a-fA-F-]{36}$")
var identityIdempotencyKeyPattern = regexp.MustCompile("^[A-Za-z0-9_-]{43}$")

const (
	maxIdentityApplicationBody = 16 << 10
	maxIdentityEvidenceBytes = int64(15 << 20)
	identityAdultThreshold = 18
	nativeIdentitySessionTTL = 24 * time.Hour
	identityEvidenceNamespace = "identity-evidence-private"
)

type identityApplicationInput struct {
	Email string `json:"email"`
	Username string `json:"username"`
	DisplayName string `json:"display_name"`
	AdultConfirmed bool `json:"adult_confirmed"`
	TermsAccepted bool `json:"terms_accepted"`
	PrivacyAccepted bool `json:"privacy_accepted"`
}

type identityEvidenceUploadInput struct {
	ContentType string `json:"content_type"`
	SizeBytes int64 `json:"size_bytes"`
	SHA256 string `json:"sha256"`
}

type identityEvidenceSubmitInput struct {
	EvidenceID string `json:"evidence_id"`
}

type identitySessionAccess struct {
	ApplicationID string
	Email string
	Status string
	Purpose string
}

func (s *service) identityApplicationCreate(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(env("TESTAGRAM_IDENTITY_REGISTRATION_ENABLED", "false"), "true") {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("REGISTRATION_DISABLED", "Identity registration is not enabled", requestID(r)))
		return
	}
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", requestID(r)))
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorBody("METHOD_NOT_ALLOWED", "POST required", requestID(r)))
		return
	}
	if s.db == nil || s.virtualStorage == nil || s.mediaReplicator == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_SERVICE_UNAVAILABLE", "Native verification requires PostgreSQL, private evidence storage and verified VPS/R2/B2 replication", requestID(r)))
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
	allowed, rateErr = s.authRateAllowed(ctx, "identity-application-email", email, 3)
	if rateErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("RATE_LIMIT_UNAVAILABLE", "Registration is temporarily unavailable", requestID(r)))
		return
	}
	if !allowed {
		writeJSON(w, http.StatusTooManyRequests, errorBody("RATE_LIMITED", "Too many registration attempts. Try again later.", requestID(r)))
		return
	}

	applicationToken, err := authRandomToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("TOKEN_FAILED", "Could not create application access token", requestID(r)))
		return
	}
	sessionToken, err := authRandomToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("TOKEN_FAILED", "Could not create verification session token", requestID(r)))
		return
	}
	submissionHash := authTokenHash(idempotencyKey)
	applicationTokenHash := authTokenHash(applicationToken)
	sessionTokenHash := authTokenHash(sessionToken)

	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Registration is temporarily unavailable", requestID(r)))
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var applicationID, applicationStatus, oldEmail, oldUsername, oldName, oldTerms, oldPrivacy string
	err = tx.QueryRow(ctx, `
		SELECT id::text,status,email,username,display_name,terms_version,privacy_version
		FROM testagram_identity.identity_applications
		WHERE submission_key_sha256=$1 FOR UPDATE
	`, submissionHash).Scan(&applicationID, &applicationStatus, &oldEmail, &oldUsername, &oldName, &oldTerms, &oldPrivacy)
	existing := err == nil
	if existing && (oldEmail != email || oldUsername != username || oldName != displayName || oldTerms != termsVersion || oldPrivacy != privacyVersion) {
		writeJSON(w, http.StatusConflict, errorBody("IDEMPOTENCY_KEY_REUSED", "Idempotency-Key was already used for a different application", requestID(r)))
		return
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Registration is temporarily unavailable", requestID(r)))
		return
	}
	if existing && applicationStatus != "pending" && applicationStatus != "verification_pending" {
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "status": applicationStatus, "message": "The existing application is already being processed."})
		return
	}
	if !existing {
		if err := tx.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&applicationID); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Registration is temporarily unavailable", requestID(r)))
			return
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO testagram_identity.identity_applications
			  (id,email,username,display_name,adult_confirmed_at,evidence_reference,
			   evidence_fingerprint_sha256,terms_version,privacy_version,terms_accepted_at,
			   privacy_accepted_at,submission_key_sha256,status_access_token_sha256,
			   status_access_token_expires_at,adult_verification_threshold,
			   adult_verification_mode,adult_verification_result,status)
			VALUES ($1::uuid,$2,$3,$4,now(),$5,NULL,$6,$7,now(),now(),$8,$9,
			        now()+interval '7 days',18,'age_only','pending','verification_pending')
		`, applicationID, email, username, displayName, "native-session-pending:"+applicationID,
			termsVersion, privacyVersion, submissionHash, applicationTokenHash)
		if err != nil {
			writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "message": "If the application is eligible, the next verification step will be sent."})
			return
		}
		for _, consent := range []struct{ kind, version string }{{"terms", termsVersion}, {"privacy", privacyVersion}, {"age_confirmation", "age-v1"}} {
			if _, err := tx.Exec(ctx, `
				INSERT INTO testagram_identity.legal_consent_audit(application_id,consent_type,policy_version,accepted_at,request_id)
				VALUES ($1::uuid,$2,$3,now(),$4)
			`, applicationID, consent.kind, consent.version, requestID(r)); err != nil {
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
	// Idempotent retries rotate the raw bearer token: cancel the prior open
	// session, then create a fresh single-use session with a new digest.
	if _, err := tx.Exec(ctx, `
		UPDATE testagram_identity.verification_sessions
		SET status='cancelled',updated_at=now()
		WHERE application_id=$1::uuid AND purpose='age_assurance'
		  AND status IN ('created','in_progress','submitted','manual_review')
	`, applicationID); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Verification session could not be prepared", requestID(r)))
		return
	}
	var sessionID string
	if err := tx.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&sessionID); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Verification session could not be created", requestID(r)))
		return
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO testagram_identity.verification_sessions
		  (id,application_id,session_token_sha256,purpose,requested_minimum_age,status,expires_at,assurance_method)
		VALUES ($1::uuid,$2::uuid,$3,'age_assurance',18,'in_progress',now()+interval '24 hours','manual_review')
	`, sessionID, applicationID, sessionTokenHash); err != nil {
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
		SET adult_verification_session_id=$2,adult_verification_threshold=18,
		    adult_verification_mode='age_only',adult_verification_result='pending',
		    evidence_reference=$3,status='verification_pending',updated_at=now()
		WHERE id=$1::uuid
	`, applicationID, sessionID, "native-session-pending:"+sessionID); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Verification session could not be linked", requestID(r)))
		return
	}
	if err := tx.Commit(ctx); err != nil {
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
	var status, verificationStatus, sessionID string
	var threshold int
	var updatedAt time.Time
	var storedHash []byte
	err := s.db.QueryRow(ctx, `
		SELECT status,adult_verification_result,adult_verification_threshold,updated_at,
		       status_access_token_sha256,COALESCE(adult_verification_session_id,'')
		FROM testagram_identity.identity_applications
		WHERE id=$1::uuid AND status_access_token_expires_at>now()
	`, applicationID).Scan(&status, &verificationStatus, &threshold, &updatedAt, &storedHash, &sessionID)
	if err != nil || subtle.ConstantTimeCompare(storedHash, authTokenHash(token)) != 1 {
		writeJSON(w, http.StatusNotFound, errorBody("APPLICATION_NOT_FOUND", "Application was not found", requestID(r)))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"application_id": applicationID, "status": status,
		"adult_verification_status": verificationStatus,
		"adult_verification_threshold": threshold,
		"verification_session_id": sessionID,
		"updated_at": updatedAt.UTC().Format(time.RFC3339),
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
		"created_at": createdAt.UTC().Format(time.RFC3339),
		"expires_at": expiresAt.UTC().Format(time.RFC3339),
		"submitted_at": submittedAt,
	})
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
	var evidenceID string
	if err := s.db.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&evidenceID); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Evidence upload could not be prepared", requestID(r)))
		return
	}
	nonce, err := authRandomToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("TOKEN_FAILED", "Evidence upload could not be prepared", requestID(r)))
		return
	}
	input.ContentType = strings.ToLower(strings.TrimSpace(input.ContentType))
	objectKey := path.Join("identity", r.PathValue("session_id"), evidenceID+extension+"-"+nonce)
	_, err = s.db.Exec(ctx, `
		INSERT INTO testagram_identity.verification_evidence
		  (id,session_id,object_key,content_type,size_bytes,sha256)
		VALUES ($1::uuid,$2::uuid,$3,$4,$5,$6)
	`, evidenceID, r.PathValue("session_id"), objectKey, input.ContentType, input.SizeBytes, digest)
	if err != nil {
		writeJSON(w, http.StatusConflict, errorBody("EVIDENCE_ALREADY_RESERVED", "An evidence upload is already reserved for this session", requestID(r)))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	// Do not expose an internal MinIO hostname or a raw object-store capability
	// to mobile clients. The client uploads to this authenticated API route;
	// Backend streams the bounded payload to the private object store itself.
	uploadPath := "/api/identity/sessions/" + r.PathValue("session_id") + "/evidence/" + evidenceID + "/upload"
	writeJSON(w, http.StatusCreated, map[string]any{
		"evidence_id": evidenceID, "upload_url": uploadPath,
		"method": http.MethodPut, "content_type": input.ContentType,
		"size_bytes": input.SizeBytes, "expires_in_seconds": 300,
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
	if !identityApplicationIDPattern.MatchString(evidenceID) || s.virtualStorage == nil {
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
	uploadURL, err := s.virtualStorage.UploadURL(ctx, access.ApplicationID, identityEvidenceNamespace, objectKey, contentType, size)
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
	if err != nil || (access.Status != "in_progress" && access.Status != "created") {
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
	var objectKey, contentType string
	var size int64
	var digest []byte
	err = s.db.QueryRow(ctx, `
		SELECT object_key,content_type,size_bytes,sha256
		FROM testagram_identity.verification_evidence
		WHERE id=$1::uuid AND session_id=$2::uuid AND status='upload_pending'
	`, input.EvidenceID, r.PathValue("session_id")).Scan(&objectKey, &contentType, &size, &digest)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorBody("EVIDENCE_NOT_FOUND", "Evidence upload was not found", requestID(r)))
		return
	}
	if _, err := s.virtualStorage.VerifyUpload(ctx, access.ApplicationID, identityEvidenceNamespace, objectKey, contentType, size); err != nil {
		writeJSON(w, http.StatusConflict, errorBody("EVIDENCE_UPLOAD_INCOMPLETE", "Evidence upload is incomplete or does not match its declared metadata", requestID(r)))
		return
	}
	if err := s.virtualStorage.VerifyUploadChecksum(ctx, access.ApplicationID, identityEvidenceNamespace, objectKey, maxIdentityEvidenceBytes, hex.EncodeToString(digest)); err != nil {
		writeJSON(w, http.StatusConflict, errorBody("EVIDENCE_CHECKSUM_MISMATCH", "Uploaded evidence failed integrity verification", requestID(r)))
		return
	}
	if s.mediaReplicator == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("EVIDENCE_REPLICATION_UNAVAILABLE", "Identity evidence cannot be accepted until all three private storage providers are configured", requestID(r)))
		return
	}
	if _, err := s.mediaReplicator.Replicate(ctx, access.ApplicationID, identityEvidenceNamespace, objectKey, size, contentType, hex.EncodeToString(digest)); err != nil {
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
		    adult_verification_result='manual_review',status='under_review',updated_at=now()
		WHERE id=$1::uuid
	`, access.ApplicationID, evidenceRef, digest); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Evidence submission could not be committed", requestID(r)))
		return
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO testagram_identity.verification_events(session_id,event_type,actor_type,actor_reference,request_id,metadata)
		VALUES ($1::uuid,'evidence_submitted','applicant',$2,$3,jsonb_build_object('evidence_id',$4))
	`, r.PathValue("session_id"), access.ApplicationID, requestID(r), input.EvidenceID); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_AUDIT_FAILED", "Evidence submission audit could not be recorded", requestID(r)))
		return
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO testagram_identity.verification_events(session_id,event_type,actor_type,actor_reference,request_id)
		VALUES ($1::uuid,'review_requested','system','testagram-identity-service',$2)
	`, r.PathValue("session_id"), requestID(r)); err != nil {
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

