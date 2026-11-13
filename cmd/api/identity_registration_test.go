package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStartIdswyftAgeSessionRequestsVerifiedAgeOnly18(t *testing.T) {
	const applicationID = "123e4567-e89b-42d3-a456-426614174000"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/verify/initialize" {
			t.Fatalf("unexpected Idswyft request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-API-Key") != "isk_test_service_key" {
			t.Fatal("Idswyft API key header missing")
		}
		var input idswyftStartRequest
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatalf("decode Idswyft request: %v", err)
		}
		if input.UserID != applicationID || input.VerificationMode != "age_only" || input.AgeThreshold != 18 {
			t.Fatalf("unexpected Idswyft request payload: %#v", input)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"verification_id":"223e4567-e89b-42d3-a456-426614174000","verification_url":"` + "http://" + r.Host + `/user-verification?session=opaque` + `","verification_mode":"age_only","age_threshold":18}`))
	}))
	defer server.Close()

	// The response URL is bound to the configured verification origin.
	t.Setenv("IDSWYFT_API_BASE_URL", server.URL)
	t.Setenv("IDSWYFT_API_KEY", "isk_test_service_key")
	t.Setenv("IDSWYFT_VERIFICATION_ORIGIN", server.URL)
	// Replace the placeholder in the test server response without exposing a
	// token or relying on an external Idswyft instance.
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/verify/initialize" {
			t.Fatalf("unexpected Idswyft request: %s %s", r.Method, r.URL.Path)
		}
		var input idswyftStartRequest
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatalf("decode Idswyft request: %v", err)
		}
		if input.UserID != applicationID || input.VerificationMode != "age_only" || input.AgeThreshold != 18 {
			t.Fatalf("unexpected Idswyft request payload: %#v", input)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"verification_id":"223e4567-e89b-42d3-a456-426614174000","verification_url":"` + server.URL + `/user-verification?session=opaque","verification_mode":"age_only","age_threshold":18}`))
	})

	s := &service{upstream: server.Client()}
	id, verificationURL, err := s.startIdswyftAgeSession(t.Context(), applicationID)
	if err != nil {
		t.Fatalf("start Idswyft age session: %v", err)
	}
	if id != "223e4567-e89b-42d3-a456-426614174000" {
		t.Fatalf("verification ID = %q", id)
	}
	if !strings.HasPrefix(verificationURL, server.URL+"/user-verification?session=") {
		t.Fatalf("verification URL escaped configured origin: %q", verificationURL)
	}
}

func TestStartIdswyftAgeSessionRejectsWrongModeOrThreshold(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		threshold int
	}{
		{name: "full mode", mode: "full", threshold: 18},
		{name: "underage threshold", mode: "age_only", threshold: 16},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"success":true,"verification_id":"223e4567-e89b-42d3-a456-426614174000","verification_url":"` + serverURL(r) + `/user-verification?session=opaque","verification_mode":"` + tc.mode + `","age_threshold":` + fmt.Sprint(tc.threshold) + `} `))
			}))
			defer server.Close()
			t.Setenv("IDSWYFT_API_BASE_URL", server.URL)
			t.Setenv("IDSWYFT_API_KEY", "isk_test_service_key")
			t.Setenv("IDSWYFT_VERIFICATION_ORIGIN", server.URL)
			s := &service{upstream: server.Client()}
			if _, _, err := s.startIdswyftAgeSession(t.Context(), "123e4567-e89b-42d3-a456-426614174000"); err == nil {
				t.Fatal("unsafe Idswyft verification mode or threshold was accepted")
			}
		})
	}
}

func TestIdswyftWebhookSignatureIsTimingSafeAndBodyBound(t *testing.T) {
	secret := strings.Repeat("s", 40)
	body := []byte(`{"event":"verification.completed","status":"verified"}`)
	signature := idswyftWebhookSignature(body, secret)
	if !verifyIdswyftWebhookSignature(body, signature, secret) {
		t.Fatal("valid Idswyft signature rejected")
	}
	if verifyIdswyftWebhookSignature(append(body, ' '), signature, secret) {
		t.Fatal("modified body accepted")
	}
	if verifyIdswyftWebhookSignature(body, signature, strings.Repeat("x", 40)) {
		t.Fatal("wrong secret accepted")
	}
	if verifyIdswyftWebhookSignature(body, "sha256=short", secret) {
		t.Fatal("malformed signature accepted")
	}
}

func TestIdentityApplicationIdempotencyKeyRequiresHighEntropyShape(t *testing.T) {
	if !identityIdempotencyKeyPattern.MatchString("abcdefghijklmnopqrstuvwxyzABCDEFG") {
		t.Fatal("43-character URL-safe idempotency key rejected")
	}
	for _, bad := range []string{"short", strings.Repeat("a", 43), strings.Repeat("a", 42)+"!"} {
		if identityIdempotencyKeyPattern.MatchString(bad) {
			t.Fatalf("invalid idempotency key accepted: %q", bad)
		}
	}
}

func serverURL(r *http.Request) string {
	return "http://" + r.Host
}
