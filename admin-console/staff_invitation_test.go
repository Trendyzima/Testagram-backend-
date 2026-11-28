package main

import "testing"

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
