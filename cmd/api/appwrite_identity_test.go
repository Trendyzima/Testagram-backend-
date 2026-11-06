package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAppwriteProvisioningFailsClosedWhenKYCBindingDisabled(t *testing.T) {
	t.Setenv("APPWRITE_KYC_BINDING_ENABLED", "false")
	body := `{"username":"test_user","display_name":"Test User","birth_date":"1990-01-01","policy_version":"2026-09","terms_accepted":true,"privacy_accepted":true,"content_policy_accepted":true,"age_confirmed":true}`
	req := httptest.NewRequest("POST", "/api/identity/provision", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	(&service{}).provisionAppwriteIdentity(recorder, req)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s; want 503 while KYC binding is disabled", recorder.Code, recorder.Body.String())
	}
}

func TestValidateAppwriteProvisionInput(t *testing.T) {
	tests := []struct {
		name    string
		in      appwriteProvisionInput
		wantErr bool
	}{
		{"valid", appwriteProvisionInput{Username: "trendy_kenya", DisplayName: "Trendy", BirthDate: "1990-01-01", PolicyVersion: "2026-09", TermsAccepted: true, PrivacyAccepted: true, ContentPolicyAccepted: true, AgeConfirmed: true}, false},
		{"short username", appwriteProvisionInput{Username: "ab", DisplayName: "Trendy", BirthDate: "1990-01-01", PolicyVersion: "2026-09", TermsAccepted: true, PrivacyAccepted: true, ContentPolicyAccepted: true, AgeConfirmed: true}, true},
		{"unsafe username", appwriteProvisionInput{Username: "../admin", DisplayName: "Trendy", BirthDate: "1990-01-01", PolicyVersion: "2026-09", TermsAccepted: true, PrivacyAccepted: true, ContentPolicyAccepted: true, AgeConfirmed: true}, true},
		{"empty display name", appwriteProvisionInput{Username: "trendy_kenya", BirthDate: "1990-01-01", PolicyVersion: "2026-09", TermsAccepted: true, PrivacyAccepted: true, ContentPolicyAccepted: true, AgeConfirmed: true}, true},
		{"long display name", appwriteProvisionInput{Username: "trendy_kenya", DisplayName: string(make([]byte, 81)), BirthDate: "1990-01-01", PolicyVersion: "2026-09", TermsAccepted: true, PrivacyAccepted: true, ContentPolicyAccepted: true, AgeConfirmed: true}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAppwriteProvisionInput(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error=%v wantErr=%v", err, tt.wantErr)
			}
		})
	}
}

func TestDecodeAppwriteProvisionInput(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "valid", body: `{"username":"test_user","display_name":"Test User","birth_date":"1990-01-01","policy_version":"2026-09","terms_accepted":true,"privacy_accepted":true,"content_policy_accepted":true,"age_confirmed":true}`},
		{name: "unknown field", body: `{"username":"test_user","display_name":"Test User","birth_date":"1990-01-01","policy_version":"2026-09","terms_accepted":true,"privacy_accepted":true,"content_policy_accepted":true,"age_confirmed":true,"admin":true}`, wantErr: true},
		{name: "multiple values", body: `{"username":"test_user","display_name":"Test User","birth_date":"1990-01-01","policy_version":"2026-09","terms_accepted":true,"privacy_accepted":true,"content_policy_accepted":true,"age_confirmed":true} {}`, wantErr: true},
		{name: "invalid JSON", body: `{"username":`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/identity/provision", strings.NewReader(tt.body))
			_, err := decodeAppwriteProvisionInput(req)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error=%v wantErr=%v", err, tt.wantErr)
			}
		})
	}
}
