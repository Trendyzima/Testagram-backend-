package freshapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/appwritedb"
)

type identityApprovalPayload struct {
	Email         string `json:"email"`
	BirthDate     string `json:"birth_date"`
	SessionID     string `json:"session_id"`
	Fingerprint   string `json:"fingerprint"`
	Username      string `json:"username"`
	DisplayName   string `json:"display_name"`
	PolicyVersion string `json:"policy_version"`
	IssuedAt      int64  `json:"issued_at"`
	ExpiresAt     int64  `json:"expires_at"`
}

var identityFingerprintPattern = regexp.MustCompile("^[a-f0-9]{64}$")
var identitySessionPattern = regexp.MustCompile("^[A-Za-z0-9_-]{8,128}$")

func (s *Server) registerIdentityVerifiedAccount(w http.ResponseWriter, r *http.Request) {
	if s.identitySecret == "" || s.appwriteServerKey == "" {
		writeError(w, http.StatusServiceUnavailable, "Appwrite identity registration is not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 12<<10)
	var input struct {
		Token    string `json:"identity_approval_token"`
		Password string `json:"password"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid identity registration payload")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "identity registration must contain one JSON object")
		return
	}
	if len(input.Password) < 8 || len(input.Password) > 256 {
		writeError(w, http.StatusBadRequest, "password must contain between 8 and 256 characters")
		return
	}
	approval, err := s.verifyIdentityApprovalToken(input.Token)
	if err != nil {
		writeError(w, http.StatusForbidden, "identity approval token is invalid or expired")
		return
	}
	if !identityFingerprintPattern.MatchString(approval.Fingerprint) || !identitySessionPattern.MatchString(approval.SessionID) {
		writeError(w, http.StatusForbidden, "identity approval evidence reference is invalid")
		return
	}
	// A unique Appwrite index is the final race-safe guard; this bounded lookup
	// provides a clear conflict response before creating the identity.
	fingerprints, err := s.db.ListDocumentsWithQueries(r.Context(), "profiles", []string{
		mustQuery("equal", "identity_fingerprint_hash", approval.Fingerprint),
		mustQuery("limit", "", 1),
	}, 1)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "identity uniqueness check is unavailable")
		return
	}
	if len(fingerprints) != 0 {
		writeError(w, http.StatusConflict, "IDENTITY_ALREADY_REGISTERED")
		return
	}
	birthDate, err := time.Parse("2006-01-02", approval.BirthDate)
	now := time.Now().UTC()
	if err != nil || birthDate.Format("2006-01-02") != approval.BirthDate || birthDate.After(now.AddDate(-18, 0, 0)) {
		writeError(w, http.StatusForbidden, "identity approval does not satisfy the adult account policy")
		return
	}
	if approval.PolicyVersion != currentLegalPolicyVersion {
		writeError(w, http.StatusForbidden, "identity approval does not include the current legal policy version")
		return
	}
	handle := strings.ToLower(strings.TrimSpace(approval.Username))
	if len(handle) < 3 || len(handle) > 30 || !validHandle(handle) {
		writeError(w, http.StatusBadRequest, "identity registration username is invalid")
		return
	}
	displayName := strings.TrimSpace(approval.DisplayName)
	if displayName == "" || len(displayName) > 80 {
		displayName = handle
	}

	userBody, _ := json.Marshal(map[string]any{
		"userId": "unique()",
		"email": approval.Email,
		"password": input.Password,
		"name": displayName,
		"emailVerification": false,
	})
	createReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, strings.TrimRight(s.endpoint, "/")+"/users", bytes.NewReader(userBody))
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Appwrite account service unavailable")
		return
	}
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("X-Appwrite-Project", s.projectID)
	createReq.Header.Set("X-Appwrite-Key", s.appwriteServerKey)
	createResp, err := s.httpClient.Do(createReq)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Appwrite account service unavailable")
		return
	}
	defer createResp.Body.Close()
	if createResp.StatusCode == http.StatusConflict {
		writeError(w, http.StatusConflict, "EMAIL_ALREADY_REGISTERED")
		return
	}
	if createResp.StatusCode < 200 || createResp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(createResp.Body, 4<<10))
		writeError(w, http.StatusServiceUnavailable, "Appwrite account could not be created")
		return
	}
	var created struct {
		ID                string `json:"$id"`
		Email             string `json:"email"`
		EmailVerification bool   `json:"emailVerification"`
	}
	if err := json.NewDecoder(io.LimitReader(createResp.Body, 64<<10)).Decode(&created); err != nil ||
		!validUserID(created.ID) || !strings.EqualFold(created.Email, approval.Email) || created.EmailVerification {
		s.deleteAppwriteUser(created.ID)
		writeError(w, http.StatusServiceUnavailable, "Appwrite did not confirm the verified account")
		return
	}

	acceptedAt := time.Unix(approval.IssuedAt, 0).UTC().Format(time.RFC3339Nano)
	profile := map[string]any{
		"user_id": created.ID,
		"handle": handle,
		"display_name": displayName,
		"bio": "",
		"federation_enabled": false,
		"birth_date": approval.BirthDate,
		"legal_terms_accepted_at": acceptedAt,
		"legal_privacy_accepted_at": acceptedAt,
		"legal_content_policy_accepted_at": acceptedAt,
		"legal_age_confirmed_at": acceptedAt,
		"legal_policy_version": currentLegalPolicyVersion,
		"identity_verification_status": "approved",
		"identity_verified_at": acceptedAt,
		"identity_verification_id": approval.SessionID,
		"identity_fingerprint_hash": approval.Fingerprint,
		"created_at": now.Format(time.RFC3339Nano),
		"updated_at": now.Format(time.RFC3339Nano),
	}
	if _, err := s.db.CreateDocument(r.Context(), "profiles", created.ID, profile, nil); err != nil {
		s.deleteAppwriteUser(created.ID)
		var apiErr appwritedb.HTTPError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict {
			writeError(w, http.StatusConflict, "IDENTITY_ALREADY_REGISTERED")
		} else {
			writeError(w, http.StatusServiceUnavailable, "account profile could not be provisioned")
		}
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"ok": true,
		"data": map[string]any{"user_id": created.ID, "email": approval.Email, "email_verified": false, "email_verification_required": true},
		"error": nil,
	})
}

func (s *Server) deleteAppwriteUser(userID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, strings.TrimRight(s.endpoint, "/")+"/users/"+url.PathEscape(userID), nil)
	if err != nil {
		return
	}
	req.Header.Set("X-Appwrite-Project", s.projectID)
	req.Header.Set("X-Appwrite-Key", s.appwriteServerKey)
	resp, err := s.httpClient.Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
}

func (s *Server) verifyIdentityApprovalToken(raw string) (identityApprovalPayload, error) {
	parts := strings.Split(strings.TrimSpace(raw), ".")
	if len(parts) != 2 || len(parts[0]) > 4096 || len(parts[1]) != 64 {
		return identityApprovalPayload{}, errors.New("invalid identity approval token")
	}
	signature, err := hex.DecodeString(parts[1])
	if err != nil {
		return identityApprovalPayload{}, errors.New("invalid identity approval signature")
	}
	mac := hmac.New(sha256.New, []byte(s.identitySecret))
	_, _ = mac.Write([]byte("testagram:identity-approval:" + parts[0]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return identityApprovalPayload{}, errors.New("identity approval signature mismatch")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(decoded) > 2048 {
		return identityApprovalPayload{}, errors.New("invalid identity approval payload")
	}
	var payload identityApprovalPayload
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return identityApprovalPayload{}, errors.New("invalid identity approval payload")
	}
	now := time.Now().UTC().Unix()
	if payload.Email == "" || len(payload.Email) > 254 || payload.Email != strings.ToLower(strings.TrimSpace(payload.Email)) ||
		payload.IssuedAt > now+30 || payload.ExpiresAt <= now || payload.ExpiresAt-payload.IssuedAt > 15*60 {
		return identityApprovalPayload{}, errors.New("identity approval token expired or outside allowed lifetime")
	}
	return payload, nil
}

