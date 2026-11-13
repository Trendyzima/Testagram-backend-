package main

import (
	"crypto/subtle"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSubtleTokenCompare(t *testing.T) {
	digest := authTokenHash("a sufficiently long test bearer token")
	if subtleTokenCompare(digest, authTokenHash("a sufficiently long test bearer token")) != 1 {
		t.Fatal("matching token digest was rejected")
	}
	if subtleTokenCompare(digest, authTokenHash("a different sufficiently long token")) != 0 {
		t.Fatal("different token digest was accepted")
	}
	if subtleTokenCompare(nil, nil) != 0 {
		t.Fatal("empty token digests must never authenticate")
	}
}

func TestIdentityApplicationTokenFailsClosedWithoutDatabase(t *testing.T) {
	s := &service{}
	r := httptest.NewRequest(http.MethodPost, "/api/identity/applications/11111111-1111-4111-8111-111111111111/provisioning-token", nil)
	r.SetPathValue("application_id", "11111111-1111-4111-8111-111111111111")
	r.Header.Set("X-Application-Token", strings.Repeat("x", 40))
	if _, err := s.identityApplicationToken(r); err == nil {
		t.Fatal("application token authenticated without a database")
	}
}

func TestIdentityProvisioningTokenFailsClosedWithoutApplicantCredential(t *testing.T) {
	s := &service{}
	r := httptest.NewRequest(http.MethodPost, "/api/identity/applications/11111111-1111-4111-8111-111111111111/provisioning-token", nil)
	r.SetPathValue("application_id", "11111111-1111-4111-8111-111111111111")
	w := httptest.NewRecorder()
	s.identityProvisioningToken(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", w.Code)
	}
	if !strings.Contains(w.Body.String(), "APPLICATION_NOT_FOUND") {
		t.Fatalf("unexpected response: %s", w.Body.String())
	}
}

func TestIdentityProvisioningRedeemFailsClosedWithoutCredentials(t *testing.T) {
	s := &service{}
	r := httptest.NewRequest(http.MethodPost, "/api/identity/applications/11111111-1111-4111-8111-111111111111/redeem", nil)
	r.SetPathValue("application_id", "11111111-1111-4111-8111-111111111111")
	w := httptest.NewRecorder()
	s.identityProvisioningRedeem(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", w.Code)
	}
	if !strings.Contains(w.Body.String(), "PROVISIONING_AUTHORIZATION_INVALID") {
		t.Fatalf("unexpected response: %s", w.Body.String())
	}
}

func subtleTokenCompare(left, right []byte) int {
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	return subtle.ConstantTimeCompare(left, right)
}
