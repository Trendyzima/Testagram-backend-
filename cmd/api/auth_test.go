package main

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

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

func TestAuthPasswordHashAndVerify(t *testing.T) {
	hash, err := authPasswordHash("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
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
	if !strings.HasPrefix(hash, "pbkdf2-sha256$210000$") {
		t.Fatalf("unexpected hash format %q", hash)
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
	if _, err := authSignJWT("123e4567-e89b-42d3-a456-426614174000", "user@example.com", 1); err == nil {
		t.Fatal("JWT signing must fail without a dedicated secret")
	}
}

func TestAuthJWTIsAcceptedByLocalTokenVerifier(t *testing.T) {
	oldAuth, hadAuth := os.LookupEnv("TESTAGRAM_AUTH_JWT_SECRET")
	oldSupabase, hadSupabase := os.LookupEnv("SUPABASE_JWT_SECRET")
	_ = os.Setenv("TESTAGRAM_AUTH_JWT_SECRET", strings.Repeat("a", 40))
	_ = os.Unsetenv("SUPABASE_JWT_SECRET")
	t.Cleanup(func() {
		if hadAuth {
			_ = os.Setenv("TESTAGRAM_AUTH_JWT_SECRET", oldAuth)
		} else {
			_ = os.Unsetenv("TESTAGRAM_AUTH_JWT_SECRET")
		}
		if hadSupabase {
			_ = os.Setenv("SUPABASE_JWT_SECRET", oldSupabase)
		} else {
			_ = os.Unsetenv("SUPABASE_JWT_SECRET")
		}
	})
	const userID = "123e4567-e89b-42d3-a456-426614174000"
	token, err := authSignJWT(userID, "user@example.com", 1)
	if err != nil {
		t.Fatal(err)
	}
	got, err := localJWTSubject("Bearer " + token)
	if err != nil || got != userID {
		t.Fatalf("locally issued token rejected: subject=%q error=%v", got, err)
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
	} {
		req := httptest.NewRequest(http.MethodOptions, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Errorf("OPTIONS %s: got status %d, want %d", path, rec.Code, http.StatusNoContent)
		}
	}
}
