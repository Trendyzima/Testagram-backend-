package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	maxIdentityApplicationBody = 16 << 10
	maxIdswyftWebhookBody       = 64 << 10
	identityAdultThreshold      = 18
)

var identityApplicationIDPattern = regexp.MustCompile(`^[0-9a-fA-F-]{36}package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	maxIdentityApplicationBody = 16 << 10
	maxIdswyftWebhookBody       = 64 << 10
	identityAdultThreshold      = 18
)

)
var identityIdempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	maxIdentityApplicationBody = 16 << 10
	maxIdswyftWebhookBody       = 64 << 10
	identityAdultThreshold      = 18
)

)

type identityApplicationInput struct {
	Email          string `json:"email"`
	Username       string `json:"username"`
	DisplayName    string `json:"display_name"`
	AdultConfirmed bool   `json:"adult_confirmed"`
	TermsAccepted  bool   `json:"terms_accepted"`
	PrivacyAccepted bool  `json:"privacy_accepted"`
}

type idswyftStartRequest struct {
	UserID           string `json:"user_id"`
	VerificationMode string `json:"verification_mode"`
	AgeThreshold     int    `json:"age_threshold"`
}

type idswyftStartResponse struct {
	Success         bool   `json:"success"`
	VerificationID  string `json:"verification_id"`
	VerificationURL  string `json:"verification_url"`
	VerificationMode string `json:"verification_mode"`
	AgeThreshold     int    `json:"age_threshold"`
	Status           string `json:"status"`
}

type idswyftWebhookPayload struct {
	Event          string `json:"event"`
	UserID         string `json:"user_id"`
	VerificationID string `json:"verification_id"`
	Status         string `json:"status"`
	Timestamp      string `json:"timestamp"`
}

type identityReviewPayload struct {
	ApplicationID            string `json:"application_id"`
	Email                    string `json:"email"`
	Username                 string `json:"username"`
	DisplayName              string `json:"display_name"`
	EvidenceFingerprintSHA256 string `json:"evidence_fingerprint_sha256"`
	EvidenceAuditReference   string `json:"evidence_audit_reference"`
	TermsVersion             string `json:"terms_version"`
	PrivacyVersion           string `json:"privacy_version"`
}

func idswyftClient(client *http.Client) *http.Client {
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	safe := *client
	safe.Timeout = 8 * time.Second
	safe.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &safe
}

func parseConfiguredHTTPSURL(raw string, allowLocalhost bool) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("configured URL is invalid")
	}
	if parsed.Scheme != "https" {
		if !(allowLocalhost && parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1")) {
			return nil, errors.New("configured URL must use HTTPS")
		}
	}
	return parsed, nil
}

func (s *service) startIdswyftAgeSession(ctx context.Context, applicationID string) (string, string, error) {
	apiBase := strings.TrimRight(strings.TrimSpace(os.Getenv("IDSWYFT_API_BASE_URL")), "/")
	apiKey := strings.TrimSpace(os.Getenv("IDSWYFT_API_KEY"))
	verificationOrigin := strings.TrimRight(strings.TrimSpace(os.Getenv("IDSWYFT_VERIFICATION_ORIGIN")), "/")
	if apiBase == "" || apiKey == "" || verificationOrigin == "" {
		return "", "", errors.New("Idswyft verification is not configured")
	}
	baseURL, err := parseConfiguredHTTPSURL(apiBase, true)
	if err != nil {
		return "", "", err
	}
	allowedOrigin, err := parseConfiguredHTTPSURL(verificationOrigin, true)
	if err != nil || allowedOrigin.Path != "" || baseURL.Path != "" {
		return "", "", errors.New("Idswyft verification origin must be an HTTPS origin")
	}

	body, err := json.Marshal(idswyftStartRequest{
		UserID: applicationID, VerificationMode: "age_only", AgeThreshold: identityAdultThreshold,
	})
	if err != nil {
		return "", "", err
	}
	endpoint := strings.TrimRight(baseURL.String(), "/") + "/api/v2/verify/initialize"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", "", errors.New("could not construct Idswyft verification request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-API-Key", apiKey)

	resp, err := idswyftClient(s.upstream).Do(req)
	if err != nil {
		return "", "", errors.New("Idswyft verification service is unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("Idswyft verification initialization returned HTTP %d", resp.StatusCode)
	}
	var result idswyftStartResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&result); err != nil {
		return "", "", errors.New("Idswyft returned an invalid verification response")
	}
	if !result.Success || result.VerificationMode != "age_only" || result.AgeThreshold != identityAdultThreshold || len(result.VerificationID) != 36 || !identityApplicationIDPattern.MatchString(result.VerificationID) {
		return "", "", errors.New("Idswyft returned an invalid verification ID")
	}
	verificationURL, err := url.Parse(result.VerificationURL)
	if err != nil || verificationURL.User != nil || verificationURL.Fragment != "" || verificationURL.RawQuery == "" ||
		(verificationURL.Scheme != "https" && !(verificationURL.Scheme == "http" && (verificationURL.Hostname() == "localhost" || verificationURL.Hostname() == "127.0.0.1"))) || !strings.EqualFold(verificationURL.Host, allowedOrigin.Host) ||
		!strings.EqualFold(verificationURL.Scheme, allowedOrigin.Scheme) {
		return "", "", errors.New("Idswyft returned a verification URL outside the configured verification origin")
	}
	return result.VerificationID, verificationURL.String(), nil
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
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Registration is temporarily unavailable", requestID(r)))
		return
	}
	if !s.idswyftWebhookReady || strings.TrimSpace(os.Getenv("IDSWYFT_API_BASE_URL")) == "" ||
		strings.TrimSpace(os.Getenv("IDSWYFT_API_KEY")) == "" ||
		strings.TrimSpace(os.Getenv("IDSWYFT_VERIFICATION_ORIGIN")) == "" ||
		strings.TrimSpace(os.Getenv("IDSWYFT_WEBHOOK_SECRET")) == "" {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("AGE_ASSURANCE_NOT_CONFIGURED", "Secure age verification is not configured", requestID(r)))
		return
	}
	termsVersion := strings.TrimSpace(os.Getenv("TESTAGRAM_TERMS_VERSION"))
	privacyVersion := strings.TrimSpace(os.Getenv("TESTAGRAM_PRIVACY_VERSION"))
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
	ip := authClientIP(r)
	allowed, rateErr := s.authRateAllowed(ctx, "identity-application-ip", ip, 5)
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
		writeJSON(w, http.StatusInternalServerError, errorBody("TOKEN_FAILED", "Could not create an application access token", requestID(r)))
		return
	}
	submissionHash := authTokenHash(idempotencyKey)
	statusTokenHash := authTokenHash(applicationToken)

	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Registration is temporarily unavailable", requestID(r)))
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var applicationID, applicationStatus, verificationResult string
	var existingEmail, existingUsername, existingDisplayName, existingTermsVersion, existingPrivacyVersion string
	err = tx.QueryRow(ctx, `
		SELECT id::text, status, adult_verification_result, email, username, display_name, terms_version, privacy_version
		FROM testagram_identity.identity_applications
		WHERE submission_key_sha256=$1
		FOR UPDATE
	`, submissionHash).Scan(&applicationID, &applicationStatus, &verificationResult, &existingEmail, &existingUsername, &existingDisplayName, &existingTermsVersion, &existingPrivacyVersion)

	isExisting := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Registration is temporarily unavailable", requestID(r)))
		return
	}
	if !isExisting {
		if err := tx.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&applicationID); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Registration is temporarily unavailable", requestID(r)))
			return
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO testagram_identity.identity_applications
			  (id,email,username,display_name,adult_confirmed_at,evidence_reference,
			   evidence_fingerprint_sha256,terms_version,privacy_version,
			   terms_accepted_at,privacy_accepted_at,submission_key_sha256,
			   status_access_token_sha256,status_access_token_expires_at,
			   adult_verification_threshold,adult_verification_mode,adult_verification_result)
			VALUES ($1::uuid,$2,$3,$4,now(),$5,NULL,$6,$7,now(),now(),$8,$9,
			        now()+interval '7 days',18,'age_only','pending')
			ON CONFLICT DO NOTHING
			RETURNING id::text,status,adult_verification_result
		`, applicationID, email, username, displayName, "idswyft-session-pending:"+applicationID,
			termsVersion, privacyVersion, submissionHash, statusTokenHash).
			Scan(&applicationID, &applicationStatus, &verificationResult)
		if errors.Is(err, pgx.ErrNoRows) {
			// Do not reveal whether an email or username is already registered.
			writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "message": "If the application is eligible, the next verification step will be sent."})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Registration is temporarily unavailable", requestID(r)))
			return
		}
		for _, consent := range []struct{ kind, version string }{
			{"terms", termsVersion}, {"privacy", privacyVersion}, {"age_confirmation", "age-v1"},
		} {
			if _, err := tx.Exec(ctx, `
				INSERT INTO testagram_identity.legal_consent_audit
				  (application_id,consent_type,policy_version,accepted_at,request_id)
				VALUES ($1::uuid,$2,$3,now(),$4)
			`, applicationID, consent.kind, consent.version, requestID(r)); err != nil {
				writeJSON(w, http.StatusServiceUnavailable, errorBody("CONSENT_AUDIT_FAILED", "Registration consent could not be recorded", requestID(r)))
				return
			}
		}
	} else {
		if _, err := tx.Exec(ctx, `
			UPDATE testagram_identity.identity_applications
			SET status_access_token_sha256=$2, status_access_token_expires_at=now()+interval '7 days', updated_at=now()
			WHERE id=$1::uuid
		`, applicationID, statusTokenHash); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Registration is temporarily unavailable", requestID(r)))
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Registration is temporarily unavailable", requestID(r)))
		return
	}

	if isExisting && (existingEmail != email || existingUsername != username ||
		existingDisplayName != displayName || existingTermsVersion != termsVersion ||
		existingPrivacyVersion != privacyVersion) {
		writeJSON(w, http.StatusConflict, errorBody("IDEMPOTENCY_KEY_REUSED", "Idempotency-Key was already used for a different application", requestID(r)))
		return
	}
	if isExisting && applicationStatus == "cancelled" {
		_, err = s.db.Exec(ctx, `
			UPDATE testagram_identity.identity_applications
			SET status='pending', adult_verification_result='pending',
			    adult_verification_session_id=NULL, adult_verification_webhook_received_at=NULL,
			    evidence_reference=$2, evidence_fingerprint_sha256=NULL, updated_at=now()
			WHERE id=$1::uuid AND adult_verified_at IS NULL
		`, applicationID, "idswyft-session-pending:"+applicationID)
		if err != nil {
			writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "message": "If the application is eligible, the next verification step will be sent."})
			return
		}
		applicationStatus, verificationResult = "pending", "pending"
	}
	if isExisting && (applicationStatus != "pending" || verificationResult != "pending") {
		writeJSON(w, http.StatusAccepted, map[string]any{
			"ok": true, "application_id": applicationID, "application_token": applicationToken,
			"status": applicationStatus, "adult_verification_status": verificationResult,
		})
		return
	}

	verificationID, verificationURL, err := s.startIdswyftAgeSession(ctx, applicationID)
	if err != nil {
		_, _ = s.db.Exec(ctx, `
			UPDATE testagram_identity.identity_applications
			SET status='cancelled', adult_verification_result='failed', updated_at=now()
			WHERE id=$1::uuid AND adult_verified_at IS NULL
		`, applicationID)
		writeJSON(w, http.StatusServiceUnavailable, errorBody("AGE_ASSURANCE_UNAVAILABLE", "Secure age verification could not be started. Retry registration.", requestID(r)))
		return
	}
	_, err = s.db.Exec(ctx, `
		UPDATE testagram_identity.identity_applications
		SET adult_verification_session_id=$2,
		    adult_verification_threshold=$3,
		    adult_verification_mode='age_only',
		    adult_verification_result='pending',
		    evidence_reference=$4,
		    status='pending',
		    updated_at=now()
		WHERE id=$1::uuid AND adult_verified_at IS NULL
	`, applicationID, verificationID, identityAdultThreshold, "idswyft:"+verificationID)
	if err != nil {
		// The Idswyft session is not exposed to the client unless its reference
		// is durably bound to this application in PostgreSQL.
		writeJSON(w, http.StatusServiceUnavailable, errorBody("AGE_ASSURANCE_LINK_FAILED", "Secure age verification could not be linked. Retry registration.", requestID(r)))
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok": true, "application_id": applicationID, "application_token": applicationToken,
		"status": "pending", "adult_verification_status": "pending",
		"verification_url": verificationURL,
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
	var status, verificationStatus string
	var threshold int
	var updatedAt time.Time
	var storedHash []byte
	err := s.db.QueryRow(ctx, `
		SELECT status, adult_verification_result, adult_verification_threshold,
		       updated_at, status_access_token_sha256
		FROM testagram_identity.identity_applications
		WHERE id=$1::uuid AND status_access_token_expires_at>now()
	`, applicationID).Scan(&status, &verificationStatus, &threshold, &updatedAt, &storedHash)
	if err != nil || subtle.ConstantTimeCompare(storedHash, authTokenHash(token)) != 1 {
		writeJSON(w, http.StatusNotFound, errorBody("APPLICATION_NOT_FOUND", "Application was not found", requestID(r)))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"application_id": applicationID, "status": status,
		"adult_verification_status": verificationStatus,
		"adult_verification_threshold": threshold,
		"updated_at": updatedAt.UTC().Format(time.RFC3339),
	})
}

func (s *service) idswyftIdentityWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorBody("METHOD_NOT_ALLOWED", "POST required", requestID(r)))
		return
	}
	secret := strings.TrimSpace(os.Getenv("IDSWYFT_WEBHOOK_SECRET"))
	if len(secret) < 32 {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("WEBHOOK_NOT_CONFIGURED", "Verification callback is not configured", requestID(r)))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxIdswyftWebhookBody)
	defer r.Body.Close()
	raw, err := io.ReadAll(r.Body)
	if err != nil || len(raw) == 0 || len(raw) > maxIdswyftWebhookBody {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_WEBHOOK_BODY", "Webhook body is invalid", requestID(r)))
		return
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(raw)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	actual := strings.TrimSpace(r.Header.Get("X-Idswyft-Signature"))
	if len(actual) != len(expected) || subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) != 1 {
		writeJSON(w, http.StatusUnauthorized, errorBody("INVALID_WEBHOOK_SIGNATURE", "Webhook signature is invalid", requestID(r)))
		return
	}
	var event idswyftWebhookPayload
	if err := json.Unmarshal(raw, &event); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_WEBHOOK_BODY", "Webhook body is invalid", requestID(r)))
		return
	}
	if event.Event != "verification.completed" && event.Event != "verification.failed" && event.Event != "verification.manual_review" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !identityApplicationIDPattern.MatchString(event.UserID) ||
		!identityApplicationIDPattern.MatchString(event.VerificationID) || s.db == nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_WEBHOOK_REFERENCE", "Webhook reference is invalid", requestID(r)))
		return
	}
	if (event.Event == "verification.completed" && event.Status != "verified") ||
		(event.Event == "verification.failed" && event.Status != "failed") ||
		(event.Event == "verification.manual_review" && event.Status != "manual_review") {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_WEBHOOK_RESULT", "Webhook result does not match its event", requestID(r)))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_STORE_UNAVAILABLE", "Verification callback could not be recorded", requestID(r)))
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var email, username, displayName, termsVersion, privacyVersion, storedSessionID, currentStatus, currentResult, existingRequestID, verificationMode string
	var adultVerified bool
	var verificationThreshold int
	err = tx.QueryRow(ctx, `
		SELECT email, username, display_name, terms_version, privacy_version,
		       adult_verification_session_id, status, adult_verification_result,
		       adult_verified_at IS NOT NULL, COALESCE(sensitive_action_request_id::text,''),
		       adult_verification_mode, adult_verification_threshold
		FROM testagram_identity.identity_applications
		WHERE id=$1::uuid
		FOR UPDATE
	`, event.UserID).Scan(&email, &username, &displayName, &termsVersion, &privacyVersion,
		&storedSessionID, &currentStatus, &currentResult, &adultVerified, &existingRequestID, &verificationMode, &verificationThreshold)
	if err != nil || storedSessionID != event.VerificationID || verificationMode != "age_only" || verificationThreshold != identityAdultThreshold {
		writeJSON(w, http.StatusNotFound, errorBody("APPLICATION_NOT_FOUND", "Application was not found", requestID(r)))
		return
	}
	if adultVerified || currentResult != "pending" || existingRequestID != "" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if currentStatus != "pending" {
		w.WriteHeader(http.StatusOK)
		return
	}

	if event.Event == "verification.failed" {
		_, err = tx.Exec(ctx, `
			UPDATE testagram_identity.identity_applications
			SET adult_verification_result='failed', adult_verification_webhook_received_at=now(),
			    status='rejected', updated_at=now()
			WHERE id=$1::uuid
		`, event.UserID)
	} else if event.Event == "verification.manual_review" {
		_, err = tx.Exec(ctx, `
			UPDATE testagram_identity.identity_applications
			SET adult_verification_result='manual_review', adult_verification_webhook_received_at=now(),
			    updated_at=now()
			WHERE id=$1::uuid
		`, event.UserID)
	} else {
		fingerprint := sha256.Sum256(raw)
		_, err = tx.Exec(ctx, `
			UPDATE testagram_identity.identity_applications
			SET adult_verified_at=now(), adult_verification_provider='idswyft',
			    adult_verification_reference=$2, adult_verification_checked_at=now(),
			    adult_verification_webhook_received_at=now(), adult_verification_result='verified',
			    evidence_reference=$3, evidence_fingerprint_sha256=$4, updated_at=now()
			WHERE id=$1::uuid
		`, event.UserID, event.VerificationID, "idswyft:"+event.VerificationID, fingerprint[:])
		if err == nil {
			payload := identityReviewPayload{
				ApplicationID: event.UserID, Email: email, Username: username, DisplayName: displayName,
				EvidenceFingerprintSHA256: hex.EncodeToString(fingerprint[:]),
				EvidenceAuditReference: "idswyft:"+event.VerificationID,
				TermsVersion: termsVersion, PrivacyVersion: privacyVersion,
			}
			payloadBytes, marshalErr := json.Marshal(payload)
			if marshalErr != nil {
				err = marshalErr
			} else {
				payloadHash := sha256.Sum256(payloadBytes)
				var approvalRequestID string
				err = tx.QueryRow(ctx, `
					INSERT INTO testagram_auth.sensitive_action_requests
					  (action_type,requested_by_appwrite_user_id,target_reference,reason,payload,payload_sha256,required_approvals)
					VALUES ('identity.approve','testagram-identity-service',$1,
					        'Verified adult application requires independent two-person identity review',
					        $2::jsonb,$3,2)
					RETURNING id::text
				`, event.UserID, string(payloadBytes), hex.EncodeToString(payloadHash[:])).Scan(&approvalRequestID)
				if err == nil {
					_, err = tx.Exec(ctx, `
						INSERT INTO testagram_auth.admin_security_audit
						  (actor_appwrite_user_id,action,target_reference,request_id,metadata)
						VALUES ('testagram-identity-service','sensitive_action.requested',$1,$2,
						        jsonb_build_object('action_type','identity.approve','source','idswyft_webhook'))
					`, event.UserID, approvalRequestID)
				}
				if err == nil {
					_, err = tx.Exec(ctx, `
						UPDATE testagram_identity.identity_applications
						SET sensitive_action_request_id=$2::uuid, updated_at=now()
						WHERE id=$1::uuid AND sensitive_action_request_id IS NULL
					`, event.UserID, approvalRequestID)
				}
			}
		}
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_CALLBACK_FAILED", "Verification callback could not be committed", requestID(r)))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("IDENTITY_CALLBACK_FAILED", "Verification callback could not be committed", requestID(r)))
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *service) ensureIdswyftWebhook(ctx context.Context) error {
	apiBase := strings.TrimRight(strings.TrimSpace(os.Getenv("IDSWYFT_API_BASE_URL")), "/")
	apiKey := strings.TrimSpace(os.Getenv("IDSWYFT_API_KEY"))
	secret := strings.TrimSpace(os.Getenv("IDSWYFT_WEBHOOK_SECRET"))
	publicBase := strings.TrimRight(strings.TrimSpace(os.Getenv("TESTAGRAM_PUBLIC_API_URL")), "/")
	if apiBase == "" || apiKey == "" || len(secret) < 32 || publicBase == "" {
		return errors.New("Idswyft webhook registration configuration is incomplete")
	}
	apiURL, err := parseConfiguredHTTPSURL(apiBase, false)
	if err != nil || apiURL.Path != "" {
		return errors.New("Idswyft API base must be an HTTPS origin")
	}
	if _, err := parseConfiguredHTTPSURL(publicBase, false); err != nil {
		return err
	}
	targetURL := publicBase + "/internal/v1/identity/idswyft-webhook"
	client := idswyftClient(s.upstream)
	request := func(method, path string, body any) (*http.Response, error) {
		var reader io.Reader
		if body != nil {
			encoded, err := json.Marshal(body)
			if err != nil { return nil, err }
			reader = bytes.NewReader(encoded)
		}
		req, err := http.NewRequestWithContext(ctx, method, apiBase+path, reader)
		if err != nil { return nil, err }
		req.Header.Set("X-API-Key", apiKey)
		req.Header.Set("Accept", "application/json")
		if body != nil { req.Header.Set("Content-Type", "application/json") }
		return client.Do(req)
	}

	type webhookItem struct {
		ID       string   `json:"id"`
		URL      string   `json:"url"`
		IsActive bool     `json:"is_active"`
		Events   []string `json:"events"`
	}
	var list struct { Webhooks []webhookItem `json:"webhooks"` }
	resp, err := request(http.MethodGet, "/api/webhooks", nil)
	if err != nil { return errors.New("could not query Idswyft webhook configuration") }
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = resp.Body.Close()
		return fmt.Errorf("Idswyft webhook listing returned HTTP %d", resp.StatusCode)
	}
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&list)
	_ = resp.Body.Close()
	if decodeErr != nil { return errors.New("Idswyft webhook listing response is invalid") }

	requiredEvents := []string{"verification.completed", "verification.failed", "verification.manual_review"}
	for _, item := range list.Webhooks {
		if item.URL != targetURL || item.ID == "" { continue }
		if !item.IsActive {
			return errors.New("the existing Idswyft registration webhook is inactive")
		}
		for _, required := range requiredEvents {
			found := false
			for _, event := range item.Events { if event == required { found = true; break } }
			if !found { return errors.New("the existing Idswyft webhook is missing a required verification event") }
		}
		secretResp, secretErr := request(http.MethodGet, "/api/webhooks/"+url.PathEscape(item.ID)+"/secret", nil)
		if secretErr != nil { return errors.New("could not verify existing Idswyft webhook secret") }
		var secretResult struct { SecretKey string `json:"secret_key"` }
		if secretResp.StatusCode < 200 || secretResp.StatusCode >= 300 {
			_ = secretResp.Body.Close()
			return fmt.Errorf("Idswyft webhook secret lookup returned HTTP %d", secretResp.StatusCode)
		}
		secretDecodeErr := json.NewDecoder(io.LimitReader(secretResp.Body, 8<<10)).Decode(&secretResult)
		_ = secretResp.Body.Close()
		if secretDecodeErr != nil || len(secretResult.SecretKey) != len(secret) ||
			subtle.ConstantTimeCompare([]byte(secretResult.SecretKey), []byte(secret)) != 1 {
			return errors.New("configured Idswyft webhook secret does not match the existing webhook")
		}
		return nil
	}

	created, err := request(http.MethodPost, "/api/webhooks", map[string]any{
		"url": targetURL, "is_sandbox": false, "events": requiredEvents, "secret": secret,
	})
	if err != nil { return errors.New("could not register Idswyft webhook") }
	defer created.Body.Close()
	if created.StatusCode < 200 || created.StatusCode >= 300 {
		return fmt.Errorf("Idswyft webhook registration returned HTTP %d", created.StatusCode)
	}
	return nil
}