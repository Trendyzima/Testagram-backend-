package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFederationTokenMatches(t *testing.T) {
	const secret = "federation-internal-token-with-more-than-32-characters"
	tests := []struct {
		name          string
		expected      string
		authorization string
		want          bool
	}{
		{"valid bearer", secret, "Bearer " + secret, true},
		{"case insensitive scheme", secret, "bearer " + secret, true},
		{"wrong secret", secret, "Bearer wrong-secret", false},
		{"missing bearer", secret, secret, false},
		{"missing configured secret", "", "Bearer " + secret, false},
		{"short configured secret", "short", "Bearer short", false},
		{"extra authorization fields", secret, "Bearer " + secret + " extra", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := federationTokenMatches(test.expected, test.authorization); got != test.want {
				t.Fatalf("federationTokenMatches() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestFederationHandlePattern(t *testing.T) {
	for _, handle := range []string{"alice", "user_123", "UPPERCASE"} {
		if !localAccountUsernamePattern.MatchString(handle) {
			t.Errorf("valid handle %q was rejected", handle)
		}
	}
	for _, handle := range []string{"ab", "contains-dash", "bad handle", strings.Repeat("x", 31)} {
		if localAccountUsernamePattern.MatchString(handle) {
			t.Errorf("invalid handle %q was accepted", handle)
		}
	}
}

func TestFederationBackendReadCredentialIsDirectional(t *testing.T) {
	backendReadToken := strings.Repeat("b", 40)
	service := &service{federationInternalToken: backendReadToken}

	request := httptest.NewRequest(http.MethodGet, "/internal/v1/federation/readyz", nil)
	request.Header.Set("X-Testagram-API-Version", "1")
	request.Header.Set("Authorization", "Bearer "+strings.Repeat("i", 40))
	response := httptest.NewRecorder()
	service.federationInternalReady(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("ingress token status = %d, want %d", response.Code, http.StatusUnauthorized)
	}

	request = httptest.NewRequest(http.MethodGet, "/internal/v1/federation/readyz", nil)
	request.Header.Set("X-Testagram-API-Version", "1")
	request.Header.Set("Authorization", "Bearer "+backendReadToken)
	response = httptest.NewRecorder()
	service.federationInternalReady(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("Backend read token status = %d, want %d (authenticated request reaches DB readiness)", response.Code, http.StatusServiceUnavailable)
	}
}

func TestFederationBackendTokenDoesNotFallbackToIngressCredential(t *testing.T) {
	t.Setenv("FEDERATION_BACKEND_INTERNAL_TOKEN", "")
	t.Setenv("FEDERATION_INTERNAL_TOKEN", strings.Repeat("i", 40))
	if got := federationBackendTokenFromEnv(); got != "" {
		t.Fatalf("reverse-direction token fell back to ingress credential: %q", got)
	}

	want := strings.Repeat("b", 40)
	t.Setenv("FEDERATION_BACKEND_INTERNAL_TOKEN", want)
	if got := federationBackendTokenFromEnv(); got != want {
		t.Fatalf("dedicated reverse-direction token = %q, want configured token", got)
	}
}
