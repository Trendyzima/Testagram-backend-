package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchAppwriteAccountAcceptsUnverifiedSessionForConsentCollection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/account" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"$id":"user_new","email":"new@example.test","emailVerification":false,"status":true}`))
	}))
	defer server.Close()

	account, status, err := fetchAppwriteAccount(context.Background(), server.Client(), server.URL, "test-project", "valid-session-token")
	if err != nil || status != http.StatusOK {
		t.Fatalf("status=%d err=%v", status, err)
	}
	if account.ID != "user_new" || account.EmailVerification {
		t.Fatalf("unexpected account: %+v", account)
	}
}

func TestAppwriteRegistrationConsentRequiresPost(t *testing.T) {
	service := &service{}
	request := httptest.NewRequest(http.MethodGet, "/api/auth/registration-consent", nil)
	response := httptest.NewRecorder()

	service.appwriteRegistrationConsent(response, request)

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAppwriteRegistrationConsentFailsClosedWithoutDatabase(t *testing.T) {
	service := &service{}
	request := httptest.NewRequest(http.MethodPost, "/api/auth/registration-consent", strings.NewReader(`{"adult_confirmed":true,"terms_accepted":true,"privacy_accepted":true}`))
	response := httptest.NewRecorder()

	service.appwriteRegistrationConsent(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
