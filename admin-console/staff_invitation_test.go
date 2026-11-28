package main

import (
	"os"
	"strings"
	"testing"
)

func TestNormalizeStaffInvitationEmail(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
		ok    bool
	}{
		{name: "normalizes case and whitespace", input: "  Staff.Member@Testagram.example  ", want: "staff.member@testagram.example", ok: true},
		{name: "rejects display-name form", input: "Staff Member <staff@example.test>", ok: false},
		{name: "rejects malformed address", input: "not-an-email", ok: false},
		{name: "rejects empty address", input: "", ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeStaffInvitationEmail(tc.input)
			if (err == nil) != tc.ok {
				t.Fatalf("normalizeStaffInvitationEmail() error=%v; want success=%v", err, tc.ok)
			}
			if err == nil && got != tc.want {
				t.Fatalf("normalized email=%q; want %q", got, tc.want)
			}
		})
	}
}

func TestStaffInvitationRoleAllowlist(t *testing.T) {
	allowed := []string{"owner", "administrator", "identity_reviewer", "engineer", "operations", "support", "moderator"}
	for _, role := range allowed {
		if !validStaffInvitationRole(role) {
			t.Errorf("supported role %q was rejected", role)
		}
	}
	for _, role := range []string{"", "superuser", "authenticated", "owner ", "external_admin"} {
		if validStaffInvitationRole(role) {
			t.Errorf("unsupported role %q was accepted", role)
		}
	}
}

func TestStaffInvitationRevocationApprovalPermission(t *testing.T) {
	permission, err := approvalPermissionForAction("staff.invitation.revoke")
	if err != nil { t.Fatalf("revocation action must be reviewable: %v", err) }
	if permission != "staff.invite.approve" { t.Fatalf("revocation permission=%q; want staff.invite.approve", permission) }
	if _, err := approvalPermissionForAction("staff.invitation.delete"); err == nil {
		t.Fatal("unsupported invitation action was accepted")
	}
}

func TestStaffInvitationTokenIsRemovedFromBrowserURL(t *testing.T) {
	app, err := os.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read Admin Console browser application: %v", err)
	}
	source := string(app)
	capture := `if (location.pathname === "/staff/invitations/accept") {
  staffInvitationToken = recoveryToken;
  // Keep the one-time capability in memory only; do not leave it in browser
  // history, referrers, screenshots, or copied URLs while the invitee signs in.
  history.replaceState(null, "", "/staff/invitations/accept");
}`
	if !strings.Contains(source, capture) {
		t.Fatal("staff invitation token must be captured in memory and immediately removed from browser history")
	}
	if strings.Contains(source, `history.replaceState(null, "", "/staff/invitations/accept?token=`) {
		t.Fatal("staff invitation token must never be retained in the replacement URL")
	}
}
