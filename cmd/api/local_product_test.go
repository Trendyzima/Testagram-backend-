package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func makeLocalTestToken(t *testing.T, secret string, claims map[string]any) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(unsigned))
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestLocalJWTSubjectVerifiesSignatureAndExpiry(t *testing.T) {
	t.Setenv("SUPABASE_JWT_SECRET", "local-test-secret")
	sub := "123e4567-e89b-42d3-a456-426614174000"
	token := makeLocalTestToken(t, "local-test-secret", map[string]any{"sub": sub, "exp": time.Now().Add(time.Minute).Unix()})
	got, err := localJWTSubject("Bearer " + token)
	if err != nil || got != sub {
		t.Fatalf("localJWTSubject() = %q, %v", got, err)
	}
	if _, err := localJWTSubject("Bearer " + token + "x"); err == nil {
		t.Fatal("expected tampered signature to be rejected")
	}
	expired := makeLocalTestToken(t, "local-test-secret", map[string]any{"sub": sub, "exp": time.Now().Add(-time.Minute).Unix()})
	if _, err := localJWTSubject("Bearer " + expired); err == nil {
		t.Fatal("expected expired token to be rejected")
	}
}

func TestLocalProductCapabilityIsDisabledByDefault(t *testing.T) {
	t.Setenv("TESTAGRAM_LOCAL_PRODUCT_WRITES_ENABLED", "false")
	s := &service{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/capability", nil)
	if handled := s.localProductCapability(w, r, "request-1", capabilityRequest{Capability: "testagram.posts.create"}); handled {
		t.Fatal("local write must not intercept requests when the feature flag is off")
	}
}

func TestLocalProductCapabilityFailsClosedWithoutJWTSecret(t *testing.T) {
	t.Setenv("TESTAGRAM_LOCAL_PRODUCT_WRITES_ENABLED", "true")
	t.Setenv("SUPABASE_JWT_SECRET", "")
	s := &service{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/capability", nil)
	r.Header.Set("Authorization", "***")
	if handled := s.localProductCapability(w, r, "request-2", capabilityRequest{Capability: "testagram.posts.create"}); !handled {
		t.Fatal("expected local write capability to be handled")
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}
