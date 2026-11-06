package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateAppwriteProvisionInput(t *testing.T) {
	tests := []struct {
		name    string
		in      appwriteProvisionInput
		wantErr bool
	}{
		{"valid", appwriteProvisionInput{Username: "trendy_kenya", DisplayName: "Trendy"}, false},
		{"short username", appwriteProvisionInput{Username: "ab", DisplayName: "Trendy"}, true},
		{"unsafe username", appwriteProvisionInput{Username: "../admin", DisplayName: "Trendy"}, true},
		{"empty display name", appwriteProvisionInput{Username: "trendy_kenya"}, true},
		{"long display name", appwriteProvisionInput{Username: "trendy_kenya", DisplayName: string(make([]byte, 81))}, true},
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
		{name: "valid", body: `{"username":"test_user","display_name":"Test User"}`},
		{name: "unknown field", body: `{"username":"test_user","display_name":"Test User","admin":true}`, wantErr: true},
		{name: "multiple values", body: `{"username":"test_user","display_name":"Test User"} {}`, wantErr: true},
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
