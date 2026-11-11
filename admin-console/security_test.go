package main

import (
	"net/http"
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
