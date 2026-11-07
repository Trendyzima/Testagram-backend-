package freshapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func signedIdentityToken(t *testing.T, secret string, payload identityApprovalPayload) string {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("testagram:identity-approval:" + encoded))
	return encoded + "." + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyIdentityApprovalToken(t *testing.T) {
	api, mock := testServer(t)
	defer mock.Close()
	secret := strings.Repeat("s", 48)
	if err := api.WithIdentitySecret(secret); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Unix()
	payload := identityApprovalPayload{
		Email: "new@example.com", BirthDate: "1990-01-01",
		SessionID: "session_12345678", Fingerprint: strings.Repeat("a", 64),
		Username: "new_user", DisplayName: "New User", PolicyVersion: currentLegalPolicyVersion,
		IssuedAt: now, ExpiresAt: now + 900,
	}
	token := signedIdentityToken(t, secret, payload)
	got, err := api.verifyIdentityApprovalToken(token)
	if err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	if got.Email != payload.Email || got.SessionID != payload.SessionID || got.Fingerprint != payload.Fingerprint {
		t.Fatalf("decoded approval mismatch: %+v", got)
	}
}

func TestVerifyIdentityApprovalTokenRejectsTamperingAndExpiry(t *testing.T) {
	api, mock := testServer(t)
	defer mock.Close()
	secret := strings.Repeat("s", 48)
	if err := api.WithIdentitySecret(secret); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Unix()
	base := identityApprovalPayload{
		Email: "new@example.com", BirthDate: "1990-01-01",
		SessionID: "session_12345678", Fingerprint: strings.Repeat("b", 64),
		Username: "new_user", DisplayName: "New User", PolicyVersion: currentLegalPolicyVersion,
		IssuedAt: now, ExpiresAt: now + 900,
	}
	tampered := signedIdentityToken(t, secret, base)
	tampered = tampered[:len(tampered)-1] + "0"
	if _, err := api.verifyIdentityApprovalToken(tampered); err == nil {
		t.Fatal("tampered identity token was accepted")
	}
	expired := base
	expired.IssuedAt = now - 1800
	expired.ExpiresAt = now - 900
	if _, err := api.verifyIdentityApprovalToken(signedIdentityToken(t, secret, expired)); err == nil {
		t.Fatal("expired identity token was accepted")
	}
}

func TestIdentitySecretsRejectWeakOrMultilineValues(t *testing.T) {
	api, mock := testServer(t)
	defer mock.Close()
	if err := api.WithIdentitySecret(strings.Repeat("x", 31)); err == nil {
		t.Fatal("weak identity secret accepted")
	}
	if err := api.WithIdentitySecret(strings.Repeat("x", 32) + "\n"); err == nil {
		t.Fatal("multiline identity secret accepted")
	}
	if err := api.WithAppwriteServerKey("key\r\ninjected"); err == nil {
		t.Fatal("multiline Appwrite key accepted")
	}
}
