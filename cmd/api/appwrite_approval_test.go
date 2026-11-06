package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeAppwriteApprovalInput(t *testing.T) {
	tests := []struct {
		name string
		body string
		wantErr bool
	}{
		{"valid", `{"appwrite_user_id":"appwrite-user-1","registration_id":"reg-123","provider_event_id":"event-123"}`, false},
		{"unknown field", `{"appwrite_user_id":"appwrite-user-1","registration_id":"reg-123","provider_event_id":"event-123","approved":true}`, true},
		{"multiple values", `{"appwrite_user_id":"appwrite-user-1","registration_id":"reg-123","provider_event_id":"event-123"} {}`, true},
		{"malformed", `{"appwrite_user_id":`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/internal/identity/approval", strings.NewReader(tt.body))
			_, err := decodeAppwriteApprovalInput(req)
			if (err != nil) != tt.wantErr { t.Fatalf("error=%v wantErr=%v", err, tt.wantErr) }
		})
	}
}
