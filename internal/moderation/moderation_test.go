package moderation

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPModerationServiceClassifiesAndDerivesActionFromScore(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer moderation-test-key-123" {
			t.Fatal("moderation authorization header missing")
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"overall_score\":55,\"categories\":{\"hate_speech\":0,\"harassment\":55,\"explicit_content\":0,\"violence\":0,\"spam\":0,\"misinformation\":0},\"action\":\"pass\",\"reason\":\"needs review\"}"}}]}`))
	}))
	defer server.Close()

	service, err := NewHTTPModerationService(server.URL+"/v1", "moderation-test-key-123", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Analyze(context.Background(), "This post needs review")
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "flag" || result.OverallScore != 55 || result.Categories["harassment"] != 55 {
		t.Fatalf("unexpected moderation result: %#v", result)
	}
}

func TestHTTPModerationServiceFailsClosedOnProviderAndMalformedResponses(t *testing.T) {
	for _, mode := range []string{"provider", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "provider" {
					http.Error(w, "provider unavailable", http.StatusBadGateway)
					return
				}
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{}"}}]}`))
			}))
			defer server.Close()
			service, err := NewHTTPModerationService(server.URL+"/v1", "moderation-test-key-123", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.Analyze(context.Background(), "A post to classify"); err == nil {
				t.Fatal("moderation failure was treated as approval")
			}
		})
	}
}

func TestHTTPModerationServiceRejectsInvalidConfigurationAndContent(t *testing.T) {
	if _, err := NewHTTPModerationService("https://moderation.example/v1", "short", nil); err == nil {
		t.Fatal("short API key accepted")
	}
	service, err := NewHTTPModerationService("https://moderation.example/v1", strings.Repeat("k", 24), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Analyze(context.Background(), ""); err == nil {
		t.Fatal("empty content accepted")
	}
	short, err := service.Analyze(context.Background(), "x")
	if err != nil || short.Action != "pass" {
		t.Fatalf("short post should pass without provider analysis: %#v err=%v", short, err)
	}
}
