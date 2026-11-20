package main

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func testApp(t *testing.T) *app {
	t.Helper()
	key, err := hex.DecodeString(strings.Repeat("ab", 32))
	if err != nil { t.Fatal(err) }
	block, err := aes.NewCipher(key)
	if err != nil { t.Fatal(err) }
	aead, err := cipher.NewGCM(block)
	if err != nil { t.Fatal(err) }
	return &app{
		backendAPIURL: "http://api:8080",
		publicURL: "https://ops.example.test",
		publicOrigin: "https://ops.example.test",
		cookieSecure: true,
		aead: aead,
		client: &http.Client{Timeout: time.Second},
	}
}

func TestNewAppRejectsInvalidSessionKey(t *testing.T) {
	t.Setenv("ADMIN_SESSION_KEY", "too-short")
	t.Setenv("ADMIN_PUBLIC_URL", "https://ops.example.test")
	if _, err := newApp(); err == nil { t.Fatal("expected invalid session key to be rejected") }
}

func TestSessionCookieIsEncryptedAndRoundTrips(t *testing.T) {
	a := testApp(t)
	w := httptest.NewRecorder()
	want := session{AccessToken:"native-access-token",RefreshToken:"native-refresh-token",AccessExpiresAt:time.Now().Add(15*time.Minute).Unix(),RefreshExpiresAt:time.Now().Add(30*24*time.Hour).Unix()}
	if err := a.setSession(w,want); err != nil { t.Fatal(err) }
	cookies := w.Result().Cookies()
	if len(cookies)!=1 { t.Fatalf("got %d cookies",len(cookies)) }
	cookie:=cookies[0]
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite!=http.SameSiteStrictMode { t.Fatalf("unsafe session cookie attributes: %#v",cookie) }
	if strings.Contains(cookie.Value,want.AccessToken) || strings.Contains(cookie.Value,want.RefreshToken) {
		t.Fatal("access and refresh tokens must not be visible in cookie value")
	}
	req:=httptest.NewRequest("GET","https://ops.example.test/api/session",nil)
	req.AddCookie(cookie)
	got,err:=a.readSession(req)
	if err!=nil { t.Fatal(err) }
	if got.AccessToken!=want.AccessToken || got.RefreshToken!=want.RefreshToken { t.Fatalf("session mismatch: %#v",got) }
}

func TestSessionCookieRejectsTampering(t *testing.T) {
	a:=testApp(t)
	w:=httptest.NewRecorder()
	if err:=a.setSession(w,session{AccessToken:"native-access-token",RefreshToken:"native-refresh-token",AccessExpiresAt:time.Now().Add(15*time.Minute).Unix(),RefreshExpiresAt:time.Now().Add(30*24*time.Hour).Unix()});err!=nil{t.Fatal(err)}
	cookie:=w.Result().Cookies()[0]
	raw,err:=hex.DecodeString(cookie.Value);if err!=nil{t.Fatal(err)}
	raw[len(raw)-1]^=0x01
	cookie.Value=hex.EncodeToString(raw)
	req:=httptest.NewRequest("GET","https://ops.example.test/api/session",nil);req.AddCookie(cookie)
	if _,err:=a.readSession(req);err==nil{t.Fatal("tampered cookie must be rejected")}
}

func TestStaffRoleUsesOnlyPostgreSQLAssignedRole(t *testing.T) {
	cases := []struct{name string; user authUser; want string}{
		{"owner",authUser{StaffRole:"owner"},"owner"},
		{"approved engineer",authUser{StaffRole:"engineer"},"engineer"},
		{"identity reviewer",authUser{StaffRole:"identity_reviewer"},"identity_reviewer"},
		{"unassigned user",authUser{StaffRole:""},""},
		{"unknown role denied",authUser{StaffRole:"superuser"},""},
	}
	for _,tc:=range cases { t.Run(tc.name,func(t *testing.T){ if got:=staffRole(tc.user);got!=tc.want{t.Fatalf("staffRole()=%q want %q",got,tc.want)} }) }
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
}

func TestQueryPrometheusDistinguishesMissingSamplesFromZero(t *testing.T) {
	cases := []struct {
		name string
		body string
		want float64
		wantErr bool
	}{
		{name: "real zero is available", body: `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"0"]}]}}`, want: 0},
		{name: "positive sample", body: `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"12.5"]}]}}`, want: 12.5},
		{name: "empty vector is unavailable", body: `{"status":"success","data":{"resultType":"vector","result":[]}}`, wantErr: true},
		{name: "query error is unavailable", body: `{"status":"error","error":"bad query"}`, wantErr: true},
		{name: "malformed sample is unavailable", body: `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,{}]}]}}`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			a := testApp(t)
			a.prometheusURL = server.URL
			a.client = server.Client()
			got, err := a.queryPrometheus(t.Context(), "up")
			if (err != nil) != tc.wantErr { t.Fatalf("queryPrometheus() error = %v, wantErr %v", err, tc.wantErr) }
			if err == nil && got != tc.want { t.Fatalf("queryPrometheus() = %v, want %v", got, tc.want) }
		})
	}
}


func staffSessionCookie(t *testing.T, a *app) *http.Cookie {
	t.Helper()
	w := httptest.NewRecorder()
	s := session{
		AccessToken: strings.Repeat("a", 40),
		RefreshToken: strings.Repeat("b", 48),
		AccessExpiresAt: time.Now().Add(15 * time.Minute).Unix(),
		RefreshExpiresAt: time.Now().Add(30 * 24 * time.Hour).Unix(),
	}
	if err := a.setSession(w, s); err != nil { t.Fatal(err) }
	return w.Result().Cookies()[0]
}

func TestSignOutRetainsStaffCookieWhenBackendCannotRevoke(t *testing.T) {
	requestInfo := make(chan string, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestInfo <- r.Method + " " + r.URL.Path + " " + r.Header.Get("Authorization")
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
	}))
	defer backend.Close()
	a := testApp(t)
	a.backendAPIURL = backend.URL
	req := httptest.NewRequest(http.MethodPost, "https://ops.example.test/api/auth/sign-out", nil)
	req.AddCookie(staffSessionCookie(t, a))
	rec := httptest.NewRecorder()
	a.handleSignOut(rec, req)
	if rec.Code != http.StatusServiceUnavailable { t.Fatalf("sign-out status=%d body=%s; want 503", rec.Code, rec.Body.String()) }
	if got := <-requestInfo; got != "POST /api/auth/logout Bearer "+strings.Repeat("a", 40) { t.Fatalf("unexpected Backend revocation request: %s", got) }
	if len(rec.Result().Cookies()) != 0 { t.Fatal("staff cookie was cleared despite unconfirmed Backend revocation") }
}

func TestSignOutClearsStaffCookieAfterBackendRevocation(t *testing.T) {
	requestInfo := make(chan string, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestInfo <- r.Method + " " + r.URL.Path + " " + r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer backend.Close()
	a := testApp(t)
	a.backendAPIURL = backend.URL
	req := httptest.NewRequest(http.MethodPost, "https://ops.example.test/api/auth/sign-out", nil)
	req.AddCookie(staffSessionCookie(t, a))
	rec := httptest.NewRecorder()
	a.handleSignOut(rec, req)
	if rec.Code != http.StatusOK { t.Fatalf("sign-out status=%d body=%s", rec.Code, rec.Body.String()) }
	if got := <-requestInfo; got != "POST /api/auth/logout Bearer "+strings.Repeat("a", 40) { t.Fatalf("unexpected Backend revocation request: %s", got) }
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].MaxAge >= 0 || cookies[0].Value != "" { t.Fatalf("successful sign-out did not clear staff cookie: %#v", cookies) }
}
