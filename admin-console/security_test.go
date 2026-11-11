package main

import (
	"net/http"
	"fmt"
	"strings"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSensitiveActionPermissionMapping(t *testing.T) {
	cases := []struct {
		action string
		requestPermission string
		approvalPermission string
	}{
		{"identity.approve", "identity.review", "identity.review"},
		{"finance.execute", "finance.initiate", "finance.approve"},
		{"security.change", "security.manage", "security.manage"},
		{"deployment.production", "deployment.request", "deployment.approve"},
	}
	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			gotRequest, err := permissionForAction(tc.action)
			if err != nil || gotRequest != tc.requestPermission {
				t.Fatalf("permissionForAction() = %q, %v; want %q", gotRequest, err, tc.requestPermission)
			}
			gotApproval, err := approvalPermissionForAction(tc.action)
			if err != nil || gotApproval != tc.approvalPermission {
				t.Fatalf("approvalPermissionForAction() = %q, %v; want %q", gotApproval, err, tc.approvalPermission)
			}
		})
	}
}

func TestUnknownSensitiveActionIsRejected(t *testing.T) {
	if _, err := permissionForAction("staff.promote"); err == nil {
		t.Fatal("unknown action must not map to a permission")
	}
	if _, err := approvalPermissionForAction("staff.promote"); err == nil {
		t.Fatal("unknown approval must not map to a permission")
	}
}

func TestRecentStepUpRequiresUnexpiredWindow(t *testing.T) {
	a := testApp(t)
	for _, tc := range []struct {
		name string
		until int64
		want bool
	}{
		{"expired", time.Now().Add(-time.Second).Unix(), false},
		{"missing", 0, false},
		{"valid", time.Now().Add(time.Minute).Unix(), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			got := a.requireRecentStepUp(w, session{StepUpUntil: tc.until})
			if got != tc.want {
				t.Fatalf("requireRecentStepUp() = %v, want %v", got, tc.want)
			}
			if !tc.want && w.Code != http.StatusUnauthorized {
				t.Fatalf("expired step-up returned HTTP %d, want 401", w.Code)
			}
		})
	}
}

func TestNormalizeSensitiveDecision(t *testing.T) {
	cases := []struct {
		input string
		want string
		wantErr bool
	}{
		{"", "approved", false},
		{" approved ", "approved", false},
		{"rejected", "rejected", false},
		{"pending", "", true},
		{"APPROVED", "", true},
	}
	for _, test := range cases {
		got, err := normalizeSensitiveDecision(test.input)
		if (err != nil) != test.wantErr || got != test.want {
			t.Errorf("normalizeSensitiveDecision(%q) = %q, %v; want %q, error=%v", test.input, got, err, test.want, test.wantErr)
		}
	}
}

func TestServiceCredentialMatches(t *testing.T) {
	key:=make([]byte,32)
	for i:=range key { key[i]=byte(i+1) }
	encoded:=fmt.Sprintf("%x",key)
	if !serviceCredentialMatches(encoded,key) { t.Fatal("correct 32-byte service key was rejected") }
	if serviceCredentialMatches(encoded+"00",key) { t.Fatal("wrong-length service key was accepted") }
	if serviceCredentialMatches(strings.Repeat("0",64),key) { t.Fatal("incorrect service key was accepted") }
	if serviceCredentialMatches(encoded,nil) { t.Fatal("missing configured service key was accepted") }
	if serviceCredentialMatches(strings.Repeat("z",64),key) { t.Fatal("non-hex service key was accepted") }
}
