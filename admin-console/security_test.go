package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
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
		{"staff.webauthn.enroll", "staff.manage", "staff.manage"},
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

func TestRecentStepUpRequiresBackendSignedWebAuthnElevation(t *testing.T) {
	a := testApp(t)
	secret := strings.Repeat("s", 48)
	t.Setenv("TESTAGRAM_AUTH_JWT_SECRET", secret)
	user := authUser{ID: "11111111-1111-4111-8111-111111111111", StaffRole: "owner"}
	now := time.Now().UTC().Unix()
	accessClaims := nativeElevationClaims{
		Subject: user.ID, Email: "owner@testagram.invalid", Role: "authenticated",
		Issuer: "testagram-auth", Audience: "testagram-api", IssuedAt: now - 60,
		ExpiresAt: now + 600, TokenVersion: 3, SessionID: "33333333-3333-4333-8333-333333333333",
	}
	accessPayload, _ := json.Marshal(accessClaims)
	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(accessPayload)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(unsigned))
	accessToken := unsigned + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	token, _, err := a.mintStaffElevation(user, accessToken, []byte("test-authenticator-id"))
	if err != nil {
		t.Fatal(err)
	}
	validSession := session{AccessToken: accessToken, ElevatedToken: token}
	other := accessClaims
	other.Subject = "22222222-2222-4222-8222-222222222222"
	otherPayload, _ := json.Marshal(other)
	for _, tc := range []struct {
		name string
		s    session
		want bool
	}{
		{"valid native elevated proof", validSession, true},
		{"missing proof", session{AccessToken: accessToken}, false},
		{"tampered proof", session{AccessToken: accessToken, ElevatedToken: token + "x"}, false},
		{"proof bound to another identity", session{AccessToken: "header." + base64.RawURLEncoding.EncodeToString(otherPayload) + ".signature", ElevatedToken: token}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateRecentStepUpToken(tc.s, []byte(secret), time.Now().UTC())
			got := err == nil
			if got != tc.want {
				t.Fatalf("validateRecentStepUpToken() valid=%v want %v; err=%v", got, tc.want, err)
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

func TestStaffPermissionMutationRemainsClosedUntilTwoPersonExecutorExists(t *testing.T) {
	source, err := os.ReadFile("security.go")
	if err != nil {
		t.Fatalf("read staff security handlers: %v", err)
	}
	text := string(source)
	start := strings.Index(text, "func (a *app) handleSetStaffPermission")
	if start < 0 {
		t.Fatal("staff permission handler is missing")
	}
	relativeEnd := strings.Index(text[start:], "\nfunc (a *app) handleStaffList")
	if relativeEnd < 0 {
		t.Fatal("could not isolate staff permission handler")
	}
	handler := text[start : start+relativeEnd]
	if !strings.Contains(handler, "http.StatusNotImplemented") ||
		!strings.Contains(handler, "two-person approval executor") {
		t.Fatal("staff permission changes must remain explicitly closed until two-person execution exists")
	}
	for _, forbidden := range []string{
		"native_staff_permission_overrides",
		"INSERT INTO",
		"UPDATE testagram_auth",
	} {
		if strings.Contains(handler, forbidden) {
			t.Fatalf("staff permission handler must not mutate permissions directly; found %q", forbidden)
		}
	}
}


func TestStaffWebAuthnEnrollmentEndpointRequiresDualControlLedger(t *testing.T) {
	handlerSource, err := os.ReadFile("webauthn.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(handlerSource)
	if strings.Contains(source, "Staff enrollment authorization requires two independent Testagram approvals. No enrollment authorization was created.") {
		t.Fatal("the WebAuthn enrollment authorization endpoint is still the HTTP 501 stub")
	}
	for _, required := range []string{
		"native_sensitive_action_requests",
		"staff.webauthn.enroll",
		"required_approvals",
		"staff_webauthn_enrollment_authorization_is_valid",
		"revoked_at IS NULL",
		"consumed_at",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("WebAuthn enrollment flow is missing required guard %q", required)
		}
	}
	securitySource, err := os.ReadFile("security.go")
	if err != nil {
		t.Fatal(err)
	}
	security := string(securitySource)
	for _, required := range []string{
		"issueStaffWebAuthnEnrollmentAuthorization",
		"first_approver_user_id",
		"second_approver_user_id",
		"webauthn_enrollment_authorized",
		"FOR UPDATE OF sp,i",
	} {
		if !strings.Contains(security, required) {
			t.Fatalf("dual-control executor is missing required invariant %q", required)
		}
	}
	migration, err := os.ReadFile("../migrations/20261128_native_staff_webauthn_dual_control.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(migration)
	for _, required := range []string{
		"first_approver_user_id<>second_approver_user_id",
		"staff_webauthn_enrollment_authorization_is_valid",
		"native_sensitive_action_approvals",
		"consumed_at IS NULL AND revoked_at IS NULL",
		"staff WebAuthn enrollment authorizations are immutable audit evidence",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("dual-control migration is missing database invariant %q", required)
		}
	}
}
