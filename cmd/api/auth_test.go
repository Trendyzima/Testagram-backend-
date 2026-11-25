package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestAuthRandomOTPIsSixDigitAndHashable(t *testing.T) {
	t.Setenv("TESTAGRAM_MAIL_ENCRYPTION_KEY", strings.Repeat("ab", 32))
	seen := make(map[string]struct{})
	for i := 0; i < 100; i++ {
		code, err := authRandomOTP()
		if err != nil {
			t.Fatal(err)
		}
		if len(code) != 6 {
			t.Fatalf("OTP length = %d, want 6", len(code))
		}
		for _, digit := range code {
			if digit < '0' || digit > '9' {
				t.Fatalf("OTP contains non-digit: %q", code)
			}
		}
		hash, err := authOTPHash(code)
		if err != nil {
			t.Fatal(err)
		}
		if len(hash) != sha256.Size {
			t.Fatal("OTP HMAC must be SHA-256 sized")
		}
		seen[code] = struct{}{}
	}
	if len(seen) < 90 {
		t.Fatalf("OTP generator produced too few distinct codes: %d", len(seen))
	}
}

func TestAuthOTPHashRequiresTheExistingMailSecret(t *testing.T) {
	t.Setenv("TESTAGRAM_MAIL_ENCRYPTION_KEY", "")
	if _, err := authOTPHash("012345"); err == nil {
		t.Fatal("OTP hashing must fail closed without the mail encryption secret")
	}
}

func TestAuthPBKDF2KnownVector(t *testing.T) {
	got := authPBKDF2([]byte("password"), []byte("salt"), 1, 32)
	const want = "120fb6cffcf8b32c43e7225256c4f837a86548c92ccc35480805987cb70be17b"
	if strings.ToLower(fmtHex(got)) != want {
		t.Fatalf("PBKDF2 vector mismatch: %s", fmtHex(got))
	}
}

func fmtHex(value []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, len(value)*2)
	for i, b := range value {
		out[i*2] = digits[b>>4]
		out[i*2+1] = digits[b&15]
	}
	return string(out)
}

func TestAuthDummyPasswordHashUsesCurrentArgon2idProfile(t *testing.T) {
	if !authPasswordVerify(authDummyPasswordHash, "testagram-dummy-login-credential-not-a-real-account") {
		t.Fatal("dummy login hash does not verify with the configured Argon2id profile")
	}
	if authPasswordVerify(authDummyPasswordHash, "incorrect-dummy-password") {
		t.Fatal("dummy login hash accepted an incorrect password")
	}
}

func TestAuthPasswordHashAndVerify(t *testing.T) {
	hash, err := authPasswordHash("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("new password hash is not Argon2id: %q", hash)
	}
	if !authPasswordVerify(hash, "correct horse battery staple") {
		t.Fatal("correct password rejected")
	}
	if authPasswordVerify(hash, "wrong password") {
		t.Fatal("wrong password accepted")
	}
	if _, err := authPasswordHash("short"); err == nil {
		t.Fatal("short password accepted")
	}
	if !authPasswordNeedsRehash("pbkdf2-sha256$210000$legacy$legacy") {
		t.Fatal("legacy PBKDF2 hash should require upgrade")
	}
	if !authPasswordNeedsRehash("argon2id$v=19$m=32768,t=2,p=1$old$old") {
		t.Fatal("non-current Argon2id parameters should require upgrade")
	}
	if authPasswordNeedsRehash(hash) {
		t.Fatal("current Argon2id parameters should not require upgrade")
	}
}

func TestAuthLegacyPBKDF2Verification(t *testing.T) {
	const password = "correct horse battery staple"
	salt := []byte("0123456789abcdef")
	derived := authPBKDF2([]byte(password), salt, authPasswordRounds, 32)
	legacy := fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", authPasswordRounds,
		base64.RawURLEncoding.EncodeToString(salt), base64.RawURLEncoding.EncodeToString(derived))
	if !authPasswordVerify(legacy, password) {
		t.Fatal("legacy PBKDF2 credential rejected")
	}
	if authPasswordVerify(legacy, "incorrect password") {
		t.Fatal("legacy PBKDF2 accepted incorrect password")
	}
}

func TestArgon2idRejectsMalformedAndExcessiveParameters(t *testing.T) {
	for _, encoded := range []string{
		"argon2id$v=16$m=65536,t=3,p=2$c2FsdHNhbHRzYWx0MTIzNA$YWJj",
		"argon2id$v=19$m=999999999,t=3,p=2$c2FsdHNhbHRzYWx0MTIzNA$YWJj",
		"argon2id$v=19$m=65536,t=99,p=2$c2FsdHNhbHRzYWx0MTIzNA$YWJj",
		"argon2id$v=19$m=65536,t=3,p=255$c2FsdHNhbHRzYWx0MTIzNA$YWJj",
		"argon2id$v=19$m=65536,t=3,p=2$%%%$YWJj",
		"argon2id$v=19$m=65536,t=3,p=2$c2FsdHNhbHRzYWx0MTIzNA$YWJj",
	} {
		if authPasswordVerify(encoded, "any-password") {
			t.Fatalf("malformed Argon2id hash accepted: %q", encoded)
		}
	}
}

func TestAuthJWTRequiresDedicatedSecret(t *testing.T) {
	old, had := os.LookupEnv("TESTAGRAM_AUTH_JWT_SECRET")
	_ = os.Unsetenv("TESTAGRAM_AUTH_JWT_SECRET")
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("TESTAGRAM_AUTH_JWT_SECRET", old)
		} else {
			_ = os.Unsetenv("TESTAGRAM_AUTH_JWT_SECRET")
		}
	})
	if _, err := authSignJWT("123e4567-e89b-42d3-a456-426614174000", "user@example.com", 1, "223e4567-e89b-42d3-a456-426614174000"); err == nil {
		t.Fatal("JWT signing must fail without a dedicated secret")
	}
}

func TestAuthJWTContainsStrictSessionBoundClaims(t *testing.T) {
	oldAuth, hadAuth := os.LookupEnv("TESTAGRAM_AUTH_JWT_SECRET")
	_ = os.Setenv("TESTAGRAM_AUTH_JWT_SECRET", strings.Repeat("a", 40))
	t.Cleanup(func() {
		if hadAuth {
			_ = os.Setenv("TESTAGRAM_AUTH_JWT_SECRET", oldAuth)
		} else {
			_ = os.Unsetenv("TESTAGRAM_AUTH_JWT_SECRET")
		}
	})
	const userID = "123e4567-e89b-42d3-a456-426614174000"
	const sessionID = "223e4567-e89b-42d3-a456-426614174000"
	token, err := authSignJWT(userID, "user@example.com", 7, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT has %d segments", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims authClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Subject != userID || claims.SessionID != sessionID || claims.TokenVersion != 7 ||
		claims.Issuer != "testagram-auth" || claims.Audience != "testagram-api" ||
		claims.Role != "authenticated" || claims.ExpiresAt-claims.IssuedAt != 900 {
		t.Fatalf("unexpected signed claims: %+v", claims)
	}
	mac := hmac.New(sha256.New, []byte(strings.Repeat("a", 40)))
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(signature, mac.Sum(nil)) {
		t.Fatal("JWT signature is invalid")
	}
}

func TestAuthTokenHashIsNotRawToken(t *testing.T) {
	token := "example-recovery-token"
	got := authTokenHash(token)
	if len(got) != 32 {
		t.Fatalf("expected SHA-256 hash, got %d bytes", len(got))
	}
	if base64.RawURLEncoding.EncodeToString(got) == token {
		t.Fatal("token hash equals raw token")
	}
}

func TestAuthClientIPTrustsOnlyPrivateProxyRealIP(t *testing.T) {
	proxyRequest := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	proxyRequest.RemoteAddr = "172.20.0.4:443"
	proxyRequest.Header.Set("X-Real-IP", "203.0.113.45")
	if got := authClientIP(proxyRequest); got != "203.0.113.45" {
		t.Fatalf("trusted Caddy client IP=%q, want 203.0.113.45", got)
	}

	publicRequest := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	publicRequest.RemoteAddr = "203.0.113.10:443"
	publicRequest.Header.Set("X-Real-IP", "198.51.100.77")
	if got := authClientIP(publicRequest); got != "203.0.113.10" {
		t.Fatalf("public peer spoofed X-Real-IP: got %q", got)
	}

	invalidHeaderRequest := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	invalidHeaderRequest.RemoteAddr = "172.20.0.4:443"
	invalidHeaderRequest.Header.Set("X-Real-IP", "not-an-ip")
	if got := authClientIP(invalidHeaderRequest); got != "172.20.0.4" {
		t.Fatalf("invalid proxy client IP was accepted: got %q", got)
	}
}

func TestAuthEmailAndUsernameValidation(t *testing.T) {
	got, err := authEmail("User@example.com")
	if err != nil || got != "user@example.com" {
		t.Fatalf("email normalization failed: %q %v", got, err)
	}
	for _, bad := range []string{"", "not-an-email", "user@example.com\r\nBcc:x@y.com", "User <user@example.com>"} {
		if _, err := authEmail(bad); err == nil {
			t.Fatalf("accepted invalid email %q", bad)
		}
	}
	if !authUsernamePattern.MatchString("Test_User_7") {
		t.Fatal("valid username rejected")
	}
	if authUsernamePattern.MatchString("x") {
		t.Fatal("short username accepted")
	}
}

func TestRoutesRegisterWithoutPatternConflicts(t *testing.T) {
	// Go 1.22+ ServeMux panics at registration time for duplicate method/path
	// patterns. Keep route construction covered so an auth endpoint cannot
	// prevent the entire API process from starting.
	handler := (&service{}).routes()
	if handler == nil {
		t.Fatal("routes returned a nil handler")
	}
	for _, path := range []string{
		"/api/auth/signup",
		"/api/auth/resend-verification",
		"/api/auth/verify-email",
		"/api/auth/login",
		"/api/auth/forgot-password",
		"/api/auth/reset-password",
		"/api/auth/refresh",
		"/api/auth/logout",
		"/api/auth/logout-all",
	} {
		req := httptest.NewRequest(http.MethodOptions, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Errorf("OPTIONS %s: got status %d, want %d", path, rec.Code, http.StatusNoContent)
		}
	}
}

func TestPublicSignupRequiresIdentityReviewAndTwoPersonApproval(t *testing.T) {
	handler := (&service{}).routes()
	for _, body := range []string{
		`{"email":"applicant@example.com","username":"applicant_01","password":"a-strong-password-value"}`,
		"{invalid-json",
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/signup", strings.NewReader(body))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "IDENTITY_REVIEW_REQUIRED") {
			t.Fatalf("public signup must fail closed before parsing or touching the auth store: status=%d body=%s", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "AUTH_STORE_UNAVAILABLE") {
			t.Fatalf("public signup fell through to ordinary auth-store handling: %s", rec.Body.String())
		}
	}
}

func TestPostProvisioningEmailVerificationEndpointsAreNotBlockedBySignupGate(t *testing.T) {
	handler := (&service{}).routes()
	for _, path := range []string{"/api/auth/resend-verification", "/api/auth/verify-email"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"email":"applicant@example.com","token":"a-valid-looking-token"}`))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if strings.Contains(rec.Body.String(), "IDENTITY_REVIEW_REQUIRED") {
			t.Fatalf("POST %s was incorrectly blocked by the public signup gate: status=%d body=%s", path, rec.Code, rec.Body.String())
		}
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "AUTH_STORE_UNAVAILABLE") {
			t.Fatalf("POST %s did not reach its independent auth-store readiness boundary: status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestNativePasswordEndpointsFailClosedWhenDatabaseIsUnavailable(t *testing.T) {
	handler := (&service{}).routes()
	for _, path := range []string{
		"/api/auth/login",
		"/api/auth/forgot-password",
		"/api/auth/reset-password",
	} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"email":"user@example.com","password":"example-password","token":"example-token"}`))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "AUTH_STORE_UNAVAILABLE") {
			t.Fatalf("POST %s status=%d body=%s; expected fail-closed native auth database error", path, rec.Code, rec.Body.String())
		}
	}
}
