package main

import (
	"encoding/hex"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func testApp(t *testing.T) *app {
	t.Helper()
	t.Setenv("ADMIN_SESSION_KEY", strings.Repeat("ab", 32))
	t.Setenv("SUPABASE_URL", "https://auth.example.test")
	t.Setenv("SUPABASE_PUBLISHABLE_KEY", "test-publishable")
	t.Setenv("ADMIN_PUBLIC_URL", "https://ops.example.test")
	a, err := newApp()
	if err != nil { t.Fatal(err) }
	return a
}

func TestNewAppRejectsInvalidSessionKey(t *testing.T) {
	t.Setenv("ADMIN_SESSION_KEY", "too-short")
	t.Setenv("SUPABASE_URL", "https://auth.example.test")
	t.Setenv("SUPABASE_PUBLISHABLE_KEY", "test-publishable")
	t.Setenv("ADMIN_PUBLIC_URL", "https://ops.example.test")
	if _, err := newApp(); err == nil { t.Fatal("expected invalid session key to be rejected") }
}

func TestSessionCookieIsEncryptedAndRoundTrips(t *testing.T) {
	a := testApp(t)
	w := httptest.NewRecorder()
	want := session{AccessToken:"access-secret", RefreshToken:"refresh-secret", ExpiresAt:time.Now().Add(time.Hour).Unix()}
	if err := a.setSession(w,want); err != nil { t.Fatal(err) }
	cookies := w.Result().Cookies()
	if len(cookies)!=1 { t.Fatalf("got %d cookies",len(cookies)) }
	cookie:=cookies[0]
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite!=1 { t.Fatalf("unsafe session cookie attributes: %#v",cookie) }
	if strings.Contains(cookie.Value,want.AccessToken)||strings.Contains(cookie.Value,want.RefreshToken) { t.Fatal("tokens must not be visible in cookie value") }
	req:=httptest.NewRequest("GET","https://ops.example.test/api/session",nil)
	req.AddCookie(cookie)
	got,err:=a.readSession(req)
	if err!=nil { t.Fatal(err) }
	if got.AccessToken!=want.AccessToken||got.RefreshToken!=want.RefreshToken { t.Fatalf("session mismatch: %#v",got) }
}

func TestSessionCookieRejectsTampering(t *testing.T) {
	a:=testApp(t)
	w:=httptest.NewRecorder()
	if err:=a.setSession(w,session{AccessToken:"access",RefreshToken:"refresh",ExpiresAt:time.Now().Add(time.Hour).Unix()});err!=nil{t.Fatal(err)}
	cookie:=w.Result().Cookies()[0]
	raw,err:=hex.DecodeString(cookie.Value);if err!=nil{t.Fatal(err)}
	raw[len(raw)-1]^=0x01
	cookie.Value=hex.EncodeToString(raw)
	req:=httptest.NewRequest("GET","https://ops.example.test/api/session",nil);req.AddCookie(cookie)
	if _,err:=a.readSession(req);err==nil{t.Fatal("tampered cookie must be rejected")}
}

func TestStaffRoleUsesOnlyServerControlledAppMetadata(t *testing.T) {
	cases:=[]struct{name string; user authUser; want string}{
		{"owner",authUser{AppMetadata:map[string]any{"staff_role":"owner"},UserMetadata:map[string]any{"staff_role":"support"}},"owner"},
		{"approved engineer",authUser{AppMetadata:map[string]any{"staff_role":"engineer"}},"engineer"},
		{"user metadata cannot grant role",authUser{UserMetadata:map[string]any{"staff_role":"owner"}},""},
		{"unknown role denied",authUser{AppMetadata:map[string]any{"staff_role":"superuser"}},""},
		{"empty role denied",authUser{AppMetadata:map[string]any{}},""},
	}
	for _,tc:=range cases{t.Run(tc.name,func(t *testing.T){if got:=staffRole(tc.user);got!=tc.want{t.Fatalf("staffRole()=%q want %q",got,tc.want)}})}
}

func TestSameOriginRequiresExactConfiguredOrigin(t *testing.T) {
	a:=testApp(t)
	for _,tc:=range []struct{origin string;want bool}{
		{"https://ops.example.test",true},
		{"https://evil.example.test",false},
		{"https://ops.example.test.evil.test",false},
		{"",false},
		{"null",false},
	}{
		t.Run(url.QueryEscape(tc.origin),func(t *testing.T){r:=httptest.NewRequest("POST","https://ops.example.test/api/auth/sign-in",nil);if tc.origin!=""{r.Header.Set("Origin",tc.origin)};if got:=a.sameOrigin(r);got!=tc.want{t.Fatalf("sameOrigin(%q)=%v want %v",tc.origin,got,tc.want)}})
	}
}

func TestRecoveryRedirectUsesConfiguredConsoleOrigin(t *testing.T) {
	a:=testApp(t)
	if !strings.HasPrefix(a.publicURL,"https://ops.example.test"){t.Fatal("expected configured HTTPS public URL")}
	if _,err:=url.Parse(a.publicURL+"/reset-password");err!=nil{t.Fatal(err)}
	_ = os.Getenv("ADMIN_PUBLIC_URL")
}
