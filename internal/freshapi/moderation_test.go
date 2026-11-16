package freshapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestModerationEndpointRequiresVerifiedIdentityAndReturnsResult(t *testing.T) {
	api, mock := testServer(t)
	defer mock.Close()

	unauthorized := httptest.NewRequest(http.MethodPost, "/v1/moderation/check", strings.NewReader(`{"content":"ordinary post text"}`))
	unauthorizedRec := httptest.NewRecorder()
	api.Handler().ServeHTTP(unauthorizedRec, unauthorized)
	if unauthorizedRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized moderation status=%d", unauthorizedRec.Code)
	}

	authorized := httptest.NewRequest(http.MethodPost, "/v1/moderation/check", strings.NewReader(`{"content":"ordinary post text"}`))
	authorized.Header.Set("Authorization", "Bearer valid-session")
	authorizedRec := httptest.NewRecorder()
	api.Handler().ServeHTTP(authorizedRec, authorized)
	if authorizedRec.Code != http.StatusOK {
		t.Fatalf("moderation status=%d body=%s", authorizedRec.Code, authorizedRec.Body.String())
	}
	var envelope struct {
		Data ModerationResult `json:"data"`
	}
	if err := json.Unmarshal(authorizedRec.Body.Bytes(), &envelope); err != nil || envelope.Data.Action != "pass" {
		t.Fatalf("invalid moderation response: %#v err=%v", envelope, err)
	}
}

func TestCreatePostFailsClosedWhenModerationIsNotConfigured(t *testing.T) {
	api, mock, _ := testServerWithState(t)
	defer mock.Close()
	api.moderation = nil
	req := httptest.NewRequest(http.MethodPost, "/v1/posts", strings.NewReader(`{"content":"Hello Testagram","visibility":"public"}`))
	req.Header.Set("Authorization", "Bearer valid-session")
	req.Header.Set("Idempotency-Key", "post-create-key-001")
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("post without moderation status=%d body=%s", rec.Code, rec.Body.String())
	}
}
