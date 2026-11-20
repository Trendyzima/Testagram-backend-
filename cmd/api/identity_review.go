package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type identityReviewDecisionInput struct {
	Decision   string `json:"decision"`
	ReasonCode string `json:"reason_code"`
}

type identityApprovalInput struct {
	Reason string `json:"reason"`
}

type identityReviewSnapshot struct {
	ApplicationID             string `json:"application_id"`
	Email                     string `json:"email"`
	Username                  string `json:"username"`
	DisplayName               string `json:"display_name"`
	EvidenceFingerprintSHA256 string `json:"evidence_fingerprint_sha256"`
	EvidenceAuditReference    string `json:"evidence_audit_reference"`
	TermsVersion              string `json:"terms_version"`
	PrivacyVersion            string `json:"privacy_version"`
}

type nativeIdentityReviewer struct {
	ID    string
	Email string
}

// requireLiveIdentityReviewMFA requires a signed native access-token claim
// issued only after a real phishing-resistant challenge. An enrollment
// timestamp is not a live challenge and is never sufficient by itself.
func requireLiveIdentityReviewMFA(claims authClaims) error {
	now := time.Now().Unix()
	const maxAgeSeconds = int64(5 * 60)
	const allowedFutureClockSkewSeconds = int64(60)
	if claims.MFAAuthenticatedAt <= 0 ||
		claims.MFAAuthenticatedAt > now+allowedFutureClockSkewSeconds ||
		now-claims.MFAAuthenticatedAt > maxAgeSeconds {
		return errors.New("fresh phishing-resistant MFA proof is not present; identity review remains disabled")
	}
	return nil
}

// Identity review is authorized by Testagram-native staff principals and
// explicit, active role grants. A provider JWT or legacy staff ID is never
// accepted here. Enrollment is necessary, but a live signed MFA proof is also
// required before any queue, evidence, decision or approval operation.
func (s *service) identityReviewer(r *http.Request) (nativeIdentityReviewer, int, error) {
	if s.db == nil {
		return nativeIdentityReviewer{}, http.StatusServiceUnavailable, errors.New("identity database is unavailable")
	}
	token, err := nativeBearerToken(r)
	if err != nil {
		return nativeIdentityReviewer{}, http.StatusUnauthorized, errors.New("staff authentication required")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	claims, status, err := verifyNativeAccessToken(ctx, s.db, token)
	if err != nil {
		return nativeIdentityReviewer{}, status, errors.New("native staff session is invalid or expired")
	}
	var allowed bool
	err = s.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM testagram_auth.staff_principals sp
			JOIN testagram_auth.staff_role_grants rg
			  ON rg.user_id=sp.user_id AND rg.revoked_at IS NULL
			WHERE sp.user_id=$1::uuid
			  AND sp.enabled=TRUE
			  AND sp.mfa_enrolled_at IS NOT NULL
			  AND rg.role IN ('owner','administrator','identity_reviewer')
		)
	`, claims.Subject).Scan(&allowed)
	if err != nil {
		return nativeIdentityReviewer{}, http.StatusServiceUnavailable, errors.New("native staff authorization is unavailable")
	}
	if !allowed {
		return nativeIdentityReviewer{}, http.StatusForbidden, errors.New("enabled Testagram identity reviewer role and enrolled MFA are required")
	}
	if err := requireLiveIdentityReviewMFA(claims); err != nil {
		return nativeIdentityReviewer{}, http.StatusServiceUnavailable, err
	}
	return nativeIdentityReviewer{ID: claims.Subject, Email: claims.Email}, http.StatusOK, nil
}

func (s *service) identityReviewQueue(w http.ResponseWriter, r *http.Request) {
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", requestID(r)))
		return
	}
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorBody("METHOD_NOT_ALLOWED", "GET required", requestID(r)))
		return
	}
	reviewer, status, err := s.identityReviewer(r)
	if err != nil {
		writeJSON(w, status, errorBody("STAFF_ACCESS_DENIED", err.Error(), requestID(r)))
		return
	}
	if !s.identityEvidenceStorageConfigured() {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("PRIVATE_EVIDENCE_STORAGE_UNAVAILABLE", "Private evidence storage is unavailable", requestID(r)))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	rows, err := s.db.Query(ctx, `
		SELECT v.id::text,a.id::text,a.email,a.username,a.display_name,
		       e.id::text,e.object_key,e.content_type,e.size_bytes,v.created_at,v.expires_at
		FROM testagram_identity.verification_sessions v
		JOIN testagram_identity.identity_applications a ON a.id=v.application_id
		JOIN testagram_identity.verification_evidence e ON e.session_id=v.id AND e.status='verified'
		WHERE v.status='manual_review' AND v.expires_at>now()
		ORDER BY v.created_at ASC
		LIMIT 100
	`)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Review queue is unavailable", requestID(r)))
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var sessionID, applicationID, email, username, displayName, evidenceID, objectKey, contentType string
		var size int64
		var createdAt, expiresAt time.Time
		if err := rows.Scan(&sessionID, &applicationID, &email, &username, &displayName, &evidenceID, &objectKey, &contentType, &size, &createdAt, &expiresAt); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Review queue could not be read", requestID(r)))
			return
		}
		if _, ok := identityEvidenceExtension(contentType); !ok {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("PRIVATE_EVIDENCE_STORAGE_UNAVAILABLE", "Evidence metadata is invalid", requestID(r)))
			return
		}
		evidencePath := "/api/admin/identity/sessions/" + sessionID + "/evidence/" + evidenceID
		items = append(items, map[string]any{
			"session_id": sessionID, "application_id": applicationID, "email": email,
			"username": username, "display_name": displayName, "evidence_id": evidenceID,
			"evidence_url": evidencePath, "content_type": contentType, "size_bytes": size,
			"created_at": createdAt.UTC().Format(time.RFC3339), "expires_at": expiresAt.UTC().Format(time.RFC3339),
		})
	}
	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Review queue could not be read", requestID(r)))
		return
	}
	_ = reviewer
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func fetchIdentityEvidenceBytes(ctx context.Context, rawURL string, size int64, contentType, expectedSHA256 string) ([]byte, error) {
	if size < 1 || size > maxIdentityEvidenceBytes || len(expectedSHA256) != 64 {
		return nil, errors.New("invalid identity evidence integrity metadata")
	}
	expectedDigest, err := hex.DecodeString(expectedSHA256)
	if err != nil || len(expectedDigest) != sha256.Size {
		return nil, errors.New("invalid identity evidence SHA-256")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return nil, errors.New("private evidence provider returned an invalid URL")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, errors.New("could not create private evidence request")
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("private evidence provider is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || (response.ContentLength >= 0 && response.ContentLength != size) {
		return nil, errors.New("private evidence provider returned an invalid object response")
	}
	actualType := strings.ToLower(strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0]))
	if actualType != strings.ToLower(strings.TrimSpace(contentType)) {
		return nil, errors.New("private evidence content type does not match its verified metadata")
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, size+1))
	if err != nil || int64(len(payload)) != size {
		return nil, errors.New("private evidence byte length does not match its verified metadata")
	}
	actualDigest := sha256.Sum256(payload)
	if subtle.ConstantTimeCompare(actualDigest[:], expectedDigest) != 1 {
		return nil, errors.New("private evidence SHA-256 does not match its verified metadata")
	}
	return payload, nil
}

// identityReviewEvidence streams private evidence through the authenticated
// Backend API. It never returns a provider URL to a browser and never exposes
// the internal MinIO hostname or a reusable storage capability.
func (s *service) identityReviewEvidence(w http.ResponseWriter, r *http.Request) {
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", requestID(r)))
		return
	}
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorBody("METHOD_NOT_ALLOWED", "GET required", requestID(r)))
		return
	}
	reviewer, status, err := s.identityReviewer(r)
	if err != nil {
		writeJSON(w, status, errorBody("STAFF_ACCESS_DENIED", err.Error(), requestID(r)))
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	evidenceID := strings.TrimSpace(r.PathValue("evidence_id"))
	if !identityApplicationIDPattern.MatchString(sessionID) || !identityApplicationIDPattern.MatchString(evidenceID) || !s.identityEvidenceStorageConfigured() {
		writeJSON(w, http.StatusNotFound, errorBody("EVIDENCE_NOT_FOUND", "Evidence was not found", requestID(r)))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	var applicationID, objectKey, contentType, expectedSHA256 string
	var size int64
	err = s.db.QueryRow(ctx, `
		SELECT a.id::text,e.object_key,e.content_type,e.size_bytes,encode(e.sha256,'hex')
		FROM testagram_identity.verification_sessions v
		JOIN testagram_identity.identity_applications a ON a.id=v.application_id
		JOIN testagram_identity.verification_evidence e ON e.session_id=v.id
		WHERE v.id=$1::uuid AND e.id=$2::uuid
		  AND v.status='manual_review' AND v.expires_at>now()
		  AND e.status='verified'
	`, sessionID, evidenceID).Scan(&applicationID, &objectKey, &contentType, &size, &expectedSHA256)
	if err != nil || size < 1 || size > maxIdentityEvidenceBytes {
		writeJSON(w, http.StatusNotFound, errorBody("EVIDENCE_NOT_FOUND", "Evidence was not found", requestID(r)))
		return
	}
	// Audit the reviewer access attempt before any private evidence bytes are fetched.
	// Fail closed if the append-only audit record cannot be committed.
	if _, err := s.db.Exec(ctx, `
		INSERT INTO testagram_identity.verification_events
		  (session_id,event_type,actor_type,actor_reference,request_id,metadata)
		VALUES ($1::uuid,'evidence_access_attempted','reviewer',$2,$3,
		        jsonb_build_object('evidence_id',$4,'content_type',$5,'size_bytes',$6))
	`, sessionID, reviewer.ID, requestID(r), evidenceID, contentType, size); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_AUDIT_FAILED", "Evidence access could not be audited", requestID(r)))
		return
	}
	extension, ok := identityEvidenceExtension(contentType)
	if !ok {
		writeJSON(w, http.StatusNotFound, errorBody("EVIDENCE_NOT_FOUND", "Evidence was not found", requestID(r)))
		return
	}
	payload, err := s.identityEvidenceDownload(ctx, applicationID, objectKey, size, contentType, expectedSHA256)
	if err != nil || int64(len(payload)) != size {
		writeJSON(w, http.StatusBadGateway, errorBody("PRIVATE_EVIDENCE_STORAGE_UNAVAILABLE", "Verified evidence could not be retrieved from primary or replicas", requestID(r)))
		return
	}
	w.Header().Set("Cache-Control", "private, no-store, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", "attachment; filename=\"identity-evidence-"+evidenceID+extension+"\"")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	_, _ = w.Write(payload)
}

func (s *service) identityReviewDecision(w http.ResponseWriter, r *http.Request) {
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", requestID(r)))
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorBody("METHOD_NOT_ALLOWED", "POST required", requestID(r)))
		return
	}
	reviewer, status, err := s.identityReviewer(r)
	if err != nil {
		writeJSON(w, status, errorBody("STAFF_ACCESS_DENIED", err.Error(), requestID(r)))
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	if !identityApplicationIDPattern.MatchString(sessionID) {
		writeJSON(w, http.StatusNotFound, errorBody("SESSION_NOT_FOUND", "Verification session was not found", requestID(r)))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	defer r.Body.Close()
	var input identityReviewDecisionInput
	if err := decodeSingleJSON(r.Body, &input); err != nil ||
		(input.Decision != "verified" && input.Decision != "failed" && input.Decision != "escalate") ||
		len(strings.TrimSpace(input.ReasonCode)) < 2 || len(input.ReasonCode) > 64 {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_REVIEW_DECISION", "Decision or reason code is invalid", requestID(r)))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Review decision could not be recorded", requestID(r)))
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var applicationID, applicantEmail, evidenceRef, termsVersion, privacyVersion, username, displayName string
	var fingerprint []byte
	var currentStatus string
	err = tx.QueryRow(ctx, `
		SELECT a.id::text,a.email,a.username,a.display_name,a.terms_version,a.privacy_version,
		       a.evidence_reference,a.evidence_fingerprint_sha256,v.status
		FROM testagram_identity.verification_sessions v
		JOIN testagram_identity.identity_applications a ON a.id=v.application_id
		JOIN testagram_identity.verification_evidence e ON e.session_id=v.id AND e.status='verified'
		WHERE v.id=$1::uuid AND v.expires_at>now()
		FOR UPDATE OF v,a
	`, sessionID).Scan(&applicationID, &applicantEmail, &username, &displayName, &termsVersion, &privacyVersion, &evidenceRef, &fingerprint, &currentStatus)
	if err != nil || currentStatus != "manual_review" {
		writeJSON(w, http.StatusConflict, errorBody("SESSION_NOT_REVIEWABLE", "Verification session is not available for review", requestID(r)))
		return
	}
	if strings.EqualFold(strings.TrimSpace(applicantEmail), strings.TrimSpace(reviewer.Email)) {
		writeJSON(w, http.StatusForbidden, errorBody("SELF_REVIEW_FORBIDDEN", "Applicants cannot review their own applications", requestID(r)))
		return
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO testagram_identity.verification_reviews(session_id,reviewer_user_id,decision,reason_code,evidence_reference)
		VALUES ($1::uuid,$2,$3,$4,$5)
	`, sessionID, reviewer.ID, input.Decision, input.ReasonCode, evidenceRef); err != nil {
		writeJSON(w, http.StatusConflict, errorBody("REVIEW_ALREADY_RECORDED", "A reviewer may record only one decision per session", requestID(r)))
		return
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO testagram_identity.verification_events(session_id,event_type,actor_type,actor_reference,request_id,metadata)
		VALUES ($1::uuid,$2,'reviewer',$3,$4,jsonb_build_object('decision',$5,'reason_code',$6))
	`, sessionID, map[string]string{"verified": "decision_verified", "failed": "decision_failed", "escalate": "decision_escalated"}[input.Decision], reviewer.ID, requestID(r), input.Decision, input.ReasonCode); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_AUDIT_FAILED", "Review audit could not be recorded", requestID(r)))
		return
	}
	if input.Decision == "escalate" {
		if err := tx.Commit(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Review decision could not be committed", requestID(r)))
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "manual_review", "message": "Application escalated for additional review."})
		return
	}
	var matchingCount int
	err = tx.QueryRow(ctx, `
		SELECT count(*) FROM testagram_identity.verification_reviews
		WHERE session_id=$1::uuid AND decision=$2
	`, sessionID, input.Decision).Scan(&matchingCount)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Review decision could not be evaluated", requestID(r)))
		return
	}
	if matchingCount < 2 {
		if err := tx.Commit(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Review decision could not be committed", requestID(r)))
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "manual_review", "review_count": 1, "required_matching_reviews": 2})
		return
	}
	if input.Decision == "failed" {
		_, err = tx.Exec(ctx, `
			UPDATE testagram_identity.verification_sessions
			SET status='failed',
			    verified_age_over_threshold=CASE WHEN purpose='age_assurance' THEN FALSE ELSE NULL END,
			    assurance_level='none',
			    decided_at=now(),decided_by_user_id=$2::uuid,
			    decision_reason_code=$3,updated_at=now()
			WHERE id=$1::uuid
		`, sessionID, reviewer.ID, input.ReasonCode)
		if err == nil {
			_, err = tx.Exec(ctx, `
				UPDATE testagram_identity.identity_applications
				SET status='rejected',updated_at=now()
				WHERE id=$1::uuid
			`, applicationID)
		}
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Failed decision could not be committed", requestID(r)))
			return
		}
		if err := tx.Commit(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Failed decision could not be committed", requestID(r)))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "failed", "application_status": "rejected"})
		return
	}

	// Two matching, distinct reviewers are required before an application can
	// enter the existing two-person sensitive-action approval ledger.
	rows, err := tx.Query(ctx, `
		SELECT reviewer_user_id,reason_code
		FROM testagram_identity.verification_reviews
		WHERE session_id=$1::uuid AND decision='verified'
		ORDER BY created_at ASC LIMIT 2
	`, sessionID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Reviewer records could not be loaded", requestID(r)))
		return
	}
	type reviewerRecord struct{ id, reason string }
	approvedReviewers := make([]reviewerRecord, 0, 2)
	for rows.Next() {
		var v reviewerRecord
		if err := rows.Scan(&v.id, &v.reason); err != nil {
			rows.Close()
			writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Reviewer records could not be loaded", requestID(r)))
			return
		}
		approvedReviewers = append(approvedReviewers, v)
	}
	rows.Close()
	if len(approvedReviewers) != 2 || approvedReviewers[0].id == approvedReviewers[1].id {
		writeJSON(w, http.StatusConflict, errorBody("TWO_REVIEWERS_REQUIRED", "Two independent matching reviews are required", requestID(r)))
		return
	}
	snapshot := identityReviewSnapshot{
		ApplicationID: applicationID, Email: applicantEmail, Username: username, DisplayName: displayName,
		EvidenceFingerprintSHA256: hex.EncodeToString(fingerprint), EvidenceAuditReference: evidenceRef,
		TermsVersion: termsVersion, PrivacyVersion: privacyVersion,
	}
	payloadBytes, err := json.Marshal(snapshot)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("IDENTITY_SNAPSHOT_FAILED", "Application snapshot could not be created", requestID(r)))
		return
	}
	payloadHash := sha256.Sum256(payloadBytes)
	var approvalRequestID string
	err = tx.QueryRow(ctx, `
		INSERT INTO testagram_auth.native_sensitive_action_requests
		  (action_type,requested_by_user_id,requested_by_service,target_reference,reason,payload,payload_sha256,required_approvals)
		VALUES ('identity.approve',NULL,'identity_pipeline',$1,
		        'Verified identity application requires independent two-person account approval',
		        $2::jsonb,$3,2)
		RETURNING id::text
	`, applicationID, string(payloadBytes), hex.EncodeToString(payloadHash[:])).Scan(&approvalRequestID)
	if err == nil {
		for _, v := range approvedReviewers {
			_, err = tx.Exec(ctx, `
				INSERT INTO testagram_identity.identity_application_reviews
				  (application_id,reviewer_user_id,review_stage,decision,reason,evidence_audit_reference)
				VALUES ($1::uuid,$2,'evidence_review','approved',$3,$4)
			`, applicationID, v.id, "Independent evidence review: "+v.reason, evidenceRef)
			if err != nil {
				break
			}
		}
	}
	if err == nil {
		_, err = tx.Exec(ctx, `
			UPDATE testagram_identity.identity_applications
			SET adult_verified_at=now(),adult_verification_provider='testagram_manual',
			    adult_verification_reference=$2,adult_verification_checked_at=now(),
			    status='verified',native_sensitive_action_request_id=$3::uuid,updated_at=now()
			WHERE id=$1::uuid
		`, applicationID, evidenceRef, approvalRequestID)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `
			UPDATE testagram_identity.verification_sessions
			SET status='verified',
			    verified_age_over_threshold=CASE WHEN purpose='age_assurance' THEN TRUE ELSE NULL END,
			    assurance_level='reviewed',
			    decided_at=now(),decided_by_user_id=$2::uuid,
			    decision_reason_code=$3,updated_at=now()
			WHERE id=$1::uuid
		`, sessionID, reviewer.ID, input.ReasonCode)
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_APPROVAL_LINK_FAILED", "Verified review could not be linked to the approval workflow", requestID(r)))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_APPROVAL_LINK_FAILED", "Verified review could not be committed", requestID(r)))
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "verified", "application_status": "verified", "account_approval": "awaiting_two_person_approval"})
}
