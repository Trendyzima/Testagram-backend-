package media

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCDNIssuerSignsBackendAssertionAndValidatesScopedResponse(t *testing.T) {
	const secret = "backend-issuer-secret-0123456789abcdef"
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	key := "users/user-123/0123456789abcdef0123456789abcdef"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/media/url" {
			t.Fatalf("unexpected CDN request: %s %s", r.Method, r.URL.Path)
		}
		var assertion cdnMediaAssertion
		if err := json.NewDecoder(r.Body).Decode(&assertion); err != nil {
			t.Fatal(err)
		}
		if assertion.Issuer != cdnAssertionIssuer || assertion.Audience != cdnAssertionAudience ||
			assertion.ObjectKey != key || assertion.ViewerID != "user-123" || assertion.Visibility != "private" ||
			assertion.IssuedAt != now.Unix() || assertion.ExpiresAt != now.Add(5*time.Minute).Unix() {
			t.Fatalf("unexpected Backend assertion: %+v", assertion)
		}
		if len(assertion.Nonce) != 32 {
			t.Fatalf("nonce must contain 128 bits: %q", assertion.Nonce)
		}
		signature, err := hex.DecodeString(assertion.Signature)
		if err != nil {
			t.Fatal(err)
		}
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(cdnMediaAssertionCanonical(assertion)))
		if !hmac.Equal(signature, mac.Sum(nil)) {
			t.Fatal("Backend assertion signature did not verify")
		}
		token := strconv.FormatInt(assertion.ExpiresAt, 10) + ".user-123.private." + strings.Repeat("a", 64)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(cdnMediaURLResponse{
			URL: "https://media.test/v1/" + key + "?token=" + token,
			Path: key, ExpiresAt: assertion.ExpiresAt, Visibility: "private", ViewerID: "user-123",
		})
	}))
	defer server.Close()

	issuer, err := NewCDNIssuer("https://media.test", secret, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	issuedURL, err := issuer.Issue(context.Background(), key, "user-123", "private", 5*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(issuedURL, "https://media.test/v1/"+key+"?token=") {
		t.Fatalf("unexpected scoped CDN URL: %s", issuedURL)
	}
}

func TestCDNIssuerRejectsUnsafeConfigurationAndScope(t *testing.T) {
	for _, base := range []string{
		"http://media.test", "https://user:pass@media.test", "https://media.test/path",
		"https://media.test?token=bad", "https://media.test#fragment",
	} {
		if _, err := NewCDNIssuer(base, strings.Repeat("s", 32), nil); err == nil {
			t.Fatalf("unsafe CDN base URL accepted: %s", base)
		}
	}
	issuer, err := NewCDNIssuer("https://media.test", strings.Repeat("s", 32), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := issuer.Issue(context.Background(), "../private", "user-123", "private", time.Minute, time.Now()); err == nil {
		t.Fatal("unsafe object key accepted")
	}
	if _, err := issuer.Issue(context.Background(), "users/user-123/0123456789abcdef0123456789abcdef", "user-123", "shared", time.Minute, time.Now()); err == nil {
		t.Fatal("unsupported visibility accepted")
	}
	if _, err := issuer.Issue(context.Background(), "users/user-123/0123456789abcdef0123456789abcdef", "user-123", "private", 11*time.Minute, time.Now()); err == nil {
		t.Fatal("excessive capability lifetime accepted")
	}
}
