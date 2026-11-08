package realtime

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	issuer   = "testagram-backend"
	audience = "testagram-realtime"
	scopeConversation = "conversation:read"
	scopeMedia = "media:signal"
)

type Claims struct {
	Issuer         string `json:"iss"`
	Audience       string `json:"aud"`
	Subject        string `json:"sub"`
	ConversationID string `json:"conversation_id"`
	Scope          string `json:"scope"`
	TokenID        string `json:"jti"`
	IssuedAt       int64  `json:"iat"`
	ExpiresAt      int64  `json:"exp"`
}

func Issue(secret, subject, conversationID string, now time.Time, ttl time.Duration) (string, error) {
	return IssueScoped(secret, subject, conversationID, scopeConversation, now, ttl)
}

func IssueScoped(secret, subject, roomID, ticketScope string, now time.Time, ttl time.Duration) (string, error) {
	if len(secret) < 32 || strings.ContainsAny(secret, "\r\n") {
		return "", errors.New("realtime ticket secret must contain at least 32 characters")
	}
	subject = strings.TrimSpace(subject)
	roomID = strings.TrimSpace(roomID)
	if subject == "" || len(subject) > 128 || roomID == "" || len(roomID) > 128 || (ticketScope != scopeConversation && ticketScope != scopeMedia) {
		return "", errors.New("realtime ticket subject and conversation ID are required and bounded")
	}
	if ttl < 30*time.Second || ttl > 5*time.Minute {
		return "", errors.New("realtime ticket lifetime must be between 30 seconds and 5 minutes")
	}
	jti := make([]byte, 16)
	if _, err := rand.Read(jti); err != nil {
		return "", errors.New("could not generate realtime ticket ID")
	}
	claims := Claims{
		Issuer: issuer, Audience: audience, Subject: subject,
		ConversationID: roomID, Scope: ticketScope,
		TokenID:  base64.RawURLEncoding.EncodeToString(jti),
		IssuedAt: now.Unix(), ExpiresAt: now.Add(ttl).Unix(),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(encoded))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encoded + "." + signature, nil
}

func Verify(secret, token, expectedConversationID string, now time.Time) (Claims, error) {
	var claims Claims
	if len(secret) < 32 || strings.ContainsAny(secret, "\r\n") {
		return claims, errors.New("realtime ticket secret is not configured securely")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 || len(parts[0]) > 4096 || len(parts[1]) > 128 {
		return claims, errors.New("invalid realtime ticket")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims, errors.New("invalid realtime ticket signature")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(parts[0]))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return claims, errors.New("invalid realtime ticket signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(payload, &claims) != nil {
		return Claims{}, errors.New("invalid realtime ticket claims")
	}
	nowUnix := now.Unix()
	if claims.Issuer != issuer || claims.Audience != audience || (claims.Scope != scopeConversation && claims.Scope != scopeMedia) ||
		claims.Subject == "" || claims.ConversationID == "" || claims.TokenID == "" ||
		claims.ConversationID != expectedConversationID || claims.IssuedAt > nowUnix+30 ||
		claims.ExpiresAt <= nowUnix || claims.ExpiresAt <= claims.IssuedAt ||
		claims.ExpiresAt-claims.IssuedAt > int64(5*time.Minute/time.Second) {
		return Claims{}, errors.New("realtime ticket is expired, out of scope or invalid for this conversation")
	}
	return claims, nil
}

