package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMediaAuthenticationFailsClosedWithoutNativeIdentityStore(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/me/profile", nil)
	request.Header.Set("Authorization", "Bearer external-provider-token")
	service := &service{}

	_, status, err := service.authenticatedMediaUser(request)
	if status != http.StatusServiceUnavailable || err == nil {
		t.Fatalf("status=%d err=%v; native identity storage must be required", status, err)
	}
}

func TestNativeAuthFailureUsesProviderNeutralCodes(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		cause      error
		wantCode   string
		wantStatus int
	}{
		{"protected capability identity approval", http.StatusForbidden, errors.New("identity review and two-person account approval are required"), "IDENTITY_APPROVAL_REQUIRED", http.StatusForbidden},
		{"email verification", http.StatusForbidden, errors.New("email verification is required"), "EMAIL_VERIFICATION_REQUIRED", http.StatusForbidden},
		{"generic forbidden", http.StatusForbidden, errors.New("account is inactive"), "AUTH_FORBIDDEN", http.StatusForbidden},
		{"invalid token", http.StatusBadGateway, errors.New("native access token invalid"), "AUTH_TOKEN_INVALID", http.StatusBadGateway},
		{"identity store unavailable", http.StatusServiceUnavailable, errors.New("native identity database unavailable"), "AUTH_BACKEND_UNAVAILABLE", http.StatusServiceUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			writeNativeAuthFailure(response, test.status, test.cause, "request-test")
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.wantStatus, response.Body.String())
			}
			if !strings.Contains(response.Body.String(), test.wantCode) {
				t.Fatalf("body=%s; expected code %s", response.Body.String(), test.wantCode)
			}
			if test.name == "protected capability identity approval" && !strings.Contains(response.Body.String(), "Basic Testagram account access does not") {
				t.Fatalf("identity approval error must not imply a global account-access requirement: %s", response.Body.String())
			}
			if strings.Contains(strings.ToLower(response.Body.String()), "external identity provider") {
				t.Fatalf("non-native identity error leaked into native response: %s", response.Body.String())
			}
		})
	}
}
