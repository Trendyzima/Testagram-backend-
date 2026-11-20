package nativeauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestMintStaffElevationTokenVerifiesBaseAndAddsFreshMFAClaim(t *testing.T) {
	secret := []byte(strings.Repeat("testagram-native-auth-signing-key-", 2))
	now := time.Now().UTC()
	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	claims := elevationClaims{
		Subject: "00000000-0000-4000-8000-000000000001",
		Email: "staff@example.test", Role: "authenticated",
		Issuer: "testagram-auth", Audience: "testagram-api",
		IssuedAt: now.Unix(), ExpiresAt: now.Add(15*time.Minute).Unix(),
		TokenVersion: 1, SessionID: "00000000-0000-4000-8000-000000000002",
	}
	payload, _ := json.Marshal(claims)
	enc := base64.RawURLEncoding
	unsigned := enc.EncodeToString(header) + "." + enc.EncodeToString(payload)
	mac := hmacForTest(secret, unsigned)
	base := unsigned + "." + enc.EncodeToString(mac)

	elevated, expiresAt, err := MintStaffElevationToken(base, secret, now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(elevated, ".")
	if len(parts) != 3 {
		t.Fatalf("elevated token has %d segments", len(parts))
	}
	raw, err := enc.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var got elevationClaims
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.MFAAuthenticatedAt != now.Unix() || got.IssuedAt != now.Unix() ||
		got.ExpiresAt != expiresAt || expiresAt != now.Unix()+300 || got.JWTID == "" {
		t.Fatalf("elevation claims are incomplete or incorrectly timed: %#v", got)
	}
	if _, _, err := MintStaffElevationToken(base, secret[:16], now); err == nil {
		t.Fatal("short signing key was accepted")
	}
	tampered := parts[0] + "." + parts[1] + "." + enc.EncodeToString([]byte("invalid"))
	if _, _, err := MintStaffElevationToken(tampered, secret, now); err == nil {
		t.Fatal("invalid base-token signature was elevated")
	}
}

func hmacForTest(secret []byte, unsigned string) []byte {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(unsigned))
	return mac.Sum(nil)
}
