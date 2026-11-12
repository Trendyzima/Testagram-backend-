package freshapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"
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
	// A valid HMAC proves possession of a signing secret, not that two
	// independent authorized reviewers approved this application. Until token
	// issuance and one-time redemption are backed by the PostgreSQL review
	// ledger and provisioning saga, this endpoint must not create identities.
	writeError(w, http.StatusServiceUnavailable, "identity review registration pipeline is not enabled")
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

