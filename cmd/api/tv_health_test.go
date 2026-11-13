package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func signedTVToken(t *testing.T, key *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	encode := func(value any) string {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(data)
	}
	header := encode(map[string]string{"alg": "RS256", "kid": "tv-test-key", "typ": "JWT"})
	payload := encode(claims)
	input := header + "." + payload
	digest := sha256.Sum256([]byte(input))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func installTVTestKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tvOIDCKeys.mu.Lock()
	tvOIDCKeys.keys = map[string]*rsa.PublicKey{"tv-test-key": &key.PublicKey}
	tvOIDCKeys.expiresAt = time.Now().Add(time.Hour)
	tvOIDCKeys.mu.Unlock()
	t.Cleanup(func() {
		tvOIDCKeys.mu.Lock()
		tvOIDCKeys.keys = nil
		tvOIDCKeys.expiresAt = time.Time{}
		tvOIDCKeys.mu.Unlock()
	})
	return key
}

func validTVClaims(now time.Time) map[string]any {
	return map[string]any{
		"iss":          githubOIDCIssuer,
		"aud":          githubOIDCAudience,
		"exp":          now.Add(5 * time.Minute).Unix(),
		"nbf":          now.Add(-time.Minute).Unix(),
		"repository":   tvHealthRepository,
		"workflow":     tvHealthWorkflow,
		"workflow_ref": tvHealthRepository + "/.github/workflows/tv-health-monitor.yml@refs/heads/main",
		"ref":          "refs/heads/main",
		"event_name":   "schedule",
	}
}

func TestVerifyTVGitHubOIDCToken(t *testing.T) {
	key := installTVTestKey(t)
	now := time.Now()
	token := signedTVToken(t, key, validTVClaims(now))
	if err := verifyTVGitHubOIDCToken(context.Background(), nil, token, now); err != nil {
		t.Fatalf("valid workflow token rejected: %v", err)
	}
}

func TestVerifyTVGitHubOIDCTokenRejectsWrongWorkflowClaims(t *testing.T) {
	key := installTVTestKey(t)
	now := time.Now()
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"wrong repository", func(c map[string]any) { c["repository"] = "attacker/repo" }},
		{"wrong workflow", func(c map[string]any) { c["workflow"] = "Other workflow" }},
		{"wrong workflow file", func(c map[string]any) { c["workflow_ref"] = "attacker/repo/.github/workflows/evil.yml@refs/heads/main" }},
		{"wrong ref", func(c map[string]any) { c["ref"] = "refs/heads/feature" }},
		{"wrong audience", func(c map[string]any) { c["aud"] = "other-audience" }},
		{"wrong issuer", func(c map[string]any) { c["iss"] = "https://attacker.example" }},
		{"expired", func(c map[string]any) { c["exp"] = now.Add(-time.Minute).Unix() }},
		{"untrusted event", func(c map[string]any) { c["event_name"] = "pull_request" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			claims := validTVClaims(now)
			test.mutate(claims)
			token := signedTVToken(t, key, claims)
			if err := verifyTVGitHubOIDCToken(context.Background(), nil, token, now); err == nil {
				t.Fatal("expected token rejection")
			}
		})
	}
}

func TestVerifyTVGitHubOIDCTokenRejectsTamperingAndMalformedTokens(t *testing.T) {
	key := installTVTestKey(t)
	now := time.Now()
	token := signedTVToken(t, key, validTVClaims(now))
	for _, value := range []string{"", "not-a-jwt", token + "tampered", "a.b.c"} {
		if err := verifyTVGitHubOIDCToken(context.Background(), nil, value, now); err == nil {
			t.Fatalf("expected rejection for token %q", value)
		}
	}
}

func TestValidTVHealthInput(t *testing.T) {
	valid := tvHealthInput{ChannelID: "channel-1", URL: "https://stream.example/live.m3u8", IsOnline: true, LatencyMS: 250, Priority: 10}
	if !validTVHealthInput(valid) {
		t.Fatal("valid channel health record rejected")
	}
	invalid := []tvHealthInput{
		{ChannelID: "../bad", URL: valid.URL},
		{ChannelID: "channel-1", URL: "file:///etc/passwd"},
		{ChannelID: "channel-1", URL: "https://user:pass@stream.example/live"},
		{ChannelID: "channel-1", URL: valid.URL, LatencyMS: 30001},
		{ChannelID: "channel-1", URL: valid.URL, CheckError: string(make([]byte, 501))},
	}
	for i, item := range invalid {
		if validTVHealthInput(item) {
			t.Fatalf("invalid record %d accepted", i)
		}
	}
}

func TestTVCatalogRouteRequiresAuthorizedWorkflowIdentity(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/internal/tv/catalog", nil)
	res := httptest.NewRecorder()
	(&service{}).tvCatalogRead(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestTVHealthRouteRequiresAuthorizedWorkflowIdentity(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/internal/tv/health", nil)
	res := httptest.NewRecorder()
	(&service{}).tvHealthBatch(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
}
