package nativeauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type elevationClaims struct {
	Subject            string `json:"sub"`
	Email              string `json:"email"`
	Role               string `json:"role"`
	Issuer             string `json:"iss"`
	Audience           string `json:"aud"`
	IssuedAt           int64  `json:"iat"`
	ExpiresAt          int64  `json:"exp"`
	TokenVersion       int64  `json:"ver"`
	SessionID          string `json:"sid"`
	MFAAuthenticatedAt int64  `json:"mfa_at,omitempty"`
	MFAAuthenticatorID string `json:"mfa_credential,omitempty"`
	JWTID              string `json:"jti,omitempty"`
}

// MintStaffElevationToken adds a short-lived, signed phishing-resistant MFA
// assertion to an already-authenticated native Testagram session. Callers
// must first verify the base session and complete the WebAuthn ceremony.
func MintStaffElevationToken(accessToken, expectedSubject string, authenticatorID []byte, secret []byte, now time.Time) (string, int64, error) {
	if len(secret) < 32 || strings.TrimSpace(expectedSubject) == "" || len(authenticatorID) == 0 || len(authenticatorID) > 1024 {
		return "", 0, errors.New("native MFA signing key unavailable")
	}
	parts := strings.Split(accessToken, ".")
	if len(parts) != 3 || len(accessToken) > 8192 {
		return "", 0, errors.New("native access token is invalid")
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", 0, errors.New("native access token header is invalid")
	}
	var header struct {
		Algorithm string `json:"alg"`
		Type      string `json:"typ"`
	}
	if json.Unmarshal(headerBytes, &header) != nil || header.Algorithm != "HS256" || header.Type != "JWT" {
		return "", 0, errors.New("native access token algorithm is invalid")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", 0, errors.New("native access token signature is invalid")
	}
	baseMAC := hmac.New(sha256.New, secret)
	_, _ = baseMAC.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal(signature, baseMAC.Sum(nil)) {
		return "", 0, errors.New("native access token signature is invalid")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", 0, errors.New("native access token claims are invalid")
	}
	var claims elevationClaims
	if json.Unmarshal(payload, &claims) != nil ||
		claims.Subject != expectedSubject || claims.Issuer != "testagram-auth" ||
		claims.Audience != "testagram-api" || claims.Role != "authenticated" ||
		claims.TokenVersion < 1 || claims.SessionID == "" {
		return "", 0, errors.New("native access token cannot be elevated")
	}
	nowUnix := now.UTC().Unix()
	if claims.IssuedAt <= 0 || claims.IssuedAt > nowUnix+60 ||
		claims.ExpiresAt <= nowUnix || claims.ExpiresAt <= claims.IssuedAt ||
		claims.ExpiresAt-claims.IssuedAt > 16*60 {
		return "", 0, errors.New("native access token is expired or invalid")
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", 0, err
	}
	expiresAt := nowUnix + 5*60
	claims.IssuedAt = nowUnix
	claims.ExpiresAt = expiresAt
	claims.MFAAuthenticatedAt = nowUnix
	claims.MFAAuthenticatorID = hex.EncodeToString(authenticatorID)
	claims.JWTID = base64.RawURLEncoding.EncodeToString(nonce)
	headerBytes, _ = json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	payload, err = json.Marshal(claims)
	if err != nil {
		return "", 0, err
	}
	enc := base64.RawURLEncoding
	unsigned := enc.EncodeToString(headerBytes) + "." + enc.EncodeToString(payload)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(unsigned))
	return unsigned + "." + enc.EncodeToString(mac.Sum(nil)), expiresAt, nil
}
