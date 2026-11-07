package freshapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/appwritedb"
)

type identityApprovalPayload struct {
	Email          string `json:"email"`
	BirthDate      string `json:"birth_date"`
	SessionID      string `json:"session_id"`
	Fingerprint    string `json:"fingerprint"`
	IssuedAt       int64  `json:"issued_at"`
	ExpiresAt      int64  `json:"expires_at"`
}

var identityFingerprintPattern = regexp.MustCompile("^[a-f0-9]{64}$")
var identitySessionPattern = regexp.MustCompile("^[A-Za-z0-9_-]{8,128}$")

func (s *Server) identityApproval(w http.ResponseWriter, r *http.Request) {
	a, status, err := s.authenticate(r)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	if s.identitySecret == "" {
		writeError(w, http.StatusServiceUnavailable, "identity approval is not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var input struct {
		Token string `json:"identity_approval_token"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid identity approval payload")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "identity approval must contain one JSON object")
		return
	}
	approval, err := s.verifyIdentityApprovalToken(input.Token)
	if err != nil {
		writeError(w, http.StatusForbidden, "identity approval token is invalid or expired")
		return
	}
	if !strings.EqualFold(strings.TrimSpace(approval.Email), strings.TrimSpace(a.Email)) {
		writeError(w, http.StatusForbidden, "identity approval does not belong to this Appwrite account")
		return
	}
	birthDate, err := time.Parse("2006-01-02", approval.BirthDate)
	now := time.Now().UTC()
	if err != nil || birthDate.Format("2006-01-02") != approval.BirthDate || birthDate.After(now.AddDate(-18, 0, 0)) {
		writeError(w, http.StatusForbidden, "identity approval does not satisfy the adult account policy")
		return
	}
	if !identityFingerprintPattern.MatchString(approval.Fingerprint) || !identitySessionPattern.MatchString(approval.SessionID) {
		writeError(w, http.StatusForbidden, "identity approval evidence reference is invalid")
		return
	}
	raw, err := s.db.GetDocument(r.Context(), "profiles", a.ID)
	if err != nil {
		var apiErr appwritedb.HTTPError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			writeError(w, http.StatusConflict, "profile must be created before identity approval")
		} else {
			writeError(w, http.StatusServiceUnavailable, "profile service unavailable")
		}
		return
	}
	var profile map[string]any
	if err := json.Unmarshal(raw, &profile); err != nil {
		writeError(w, http.StatusServiceUnavailable, "profile is invalid")
		return
	}
	if existing, _ := profile["identity_verification_id"].(string); existing != "" && existing != approval.SessionID {
		writeError(w, http.StatusConflict, "this account already has a different identity verification record")
		return
	}
	verifiedAt := time.Unix(approval.IssuedAt, 0).UTC().Format(time.RFC3339Nano)
	_, err = s.db.UpdateDocument(r.Context(), "profiles", a.ID, map[string]any{
		"birth_date": approval.BirthDate,
		"identity_verification_status": "approved",
		"identity_verified_at": verifiedAt,
		"identity_verification_id": approval.SessionID,
		"identity_fingerprint_hash": approval.Fingerprint,
		"updated_at": now.Format(time.RFC3339Nano),
	})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "identity approval could not be recorded")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"status": "approved", "verified_at": verifiedAt}, "error": nil})
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
	if payload.Email == "" || payload.IssuedAt > now+30 || payload.ExpiresAt <= now || payload.ExpiresAt-payload.IssuedAt > 15*60 {
		return identityApprovalPayload{}, errors.New("identity approval token expired or outside allowed lifetime")
	}
	return payload, nil
}
