package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMailRecipientFingerprintIsStableAndCaseInsensitive(t *testing.T) {
	t.Setenv("TESTAGRAM_MAIL_ENCRYPTION_KEY", strings.Repeat("4c", 32))
	first, err := mailRecipientFingerprint("Person@Example.test")
	if err != nil {
		t.Fatal(err)
	}
	second, err := mailRecipientFingerprint("person@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("recipient fingerprint changed with email case")
	}
	if len(first) != 32 {
		t.Fatalf("fingerprint length=%d, want 32", len(first))
	}
}

func TestCloudflareMailEventRejectsMissingTokenAndUnknownEvent(t *testing.T) {
	t.Setenv("TESTAGRAM_CLOUDFLARE_EVENTS_TOKEN", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	t.Setenv("AUTH_FROM_EMAIL", "no-reply@testagram.site")
	svc := &service{}
	req := httptest.NewRequest(http.MethodPost, "/internal/mail/events", strings.NewReader(`{"type":"cf.email.sending.message.delivered"}`))
	rec := httptest.NewRecorder()
	svc.cloudflareMailEvent(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token status=%d, want 401", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/internal/mail/events", strings.NewReader(`{"type":"cf.email.sending.unknown","payload":{"eventId":"x","messageId":"y","recipient":"person@example.test"}}`))
	req.Header.Set("Authorization", "Bearer 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	rec = httptest.NewRecorder()
	svc.cloudflareMailEvent(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown event status=%d, want 400", rec.Code)
	}
}
