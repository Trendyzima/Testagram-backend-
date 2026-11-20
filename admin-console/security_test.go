package main

import (
	"net/http"
	"fmt"
	"encoding/base64"
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

func TestRecentStepUpRequiresBackendSignedWebAuthnElevation(t *testing.T) {
 a:=testApp(t)
 t.Setenv("TESTAGRAM_AUTH_JWT_SECRET",strings.Repeat("s",48))
 user:=authUser{ID:"11111111-1111-4111-8111-111111111111",StaffRole:"owner"}
 token,_,err:=a.mintStaffElevation(user);if err!=nil{t.Fatal(err)}
 accessPayload:=base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"11111111-1111-4111-8111-111111111111"}`))
 validSession:=session{AccessToken:"header."+accessPayload+".signature",ElevatedToken:token}
 for _,tc:=range []struct{name string;s session;want bool}{
  {"valid backend-signed proof",validSession,true},
  {"missing proof",session{AccessToken:validSession.AccessToken},false},
  {"tampered proof",session{AccessToken:validSession.AccessToken,ElevatedToken:token+"x"},false},
  {"proof bound to another identity",session{AccessToken:"header."+base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"22222222-2222-4222-8222-222222222222"}`))+".signature",ElevatedToken:token},false},
 }{
  t.Run(tc.name,func(t *testing.T){w:=httptest.NewRecorder();got:=a.requireRecentStepUp(w,tc.s);if got!=tc.want{t.Fatalf("requireRecentStepUp()=%v want %v; status=%d",got,tc.want,w.Code)}})
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
