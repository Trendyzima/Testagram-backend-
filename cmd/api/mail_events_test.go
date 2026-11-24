package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMailjetWebhookRequiresValidBasicAuthentication(t *testing.T) {
	t.Setenv("MAILJET_SMTP_USERNAME", "mailjet-api-key")
	t.Setenv("MAILJET_SMTP_PASSWORD", "mailjet-secret")
	svc := &service{}

	req := httptest.NewRequest(http.MethodPost, "/internal/mailjet/events", strings.NewReader("[]"))
	rec := httptest.NewRecorder()
	svc.mailjetDeliveryEvents(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing credentials status=%d, want 401", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/internal/mailjet/events", strings.NewReader("[]"))
	req.SetBasicAuth("mailjet-api-key", "wrong-secret")
	rec = httptest.NewRecorder()
	svc.mailjetDeliveryEvents(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong credentials status=%d, want 401", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/internal/mailjet/events", strings.NewReader("[]"))
	req.SetBasicAuth("mailjet-api-key", "mailjet-secret")
	rec = httptest.NewRecorder()
	svc.mailjetDeliveryEvents(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("authenticated request without database status=%d, want 503", rec.Code)
	}
}

func TestMailjetEventFingerprintIsStableAndEventSpecific(t *testing.T) {
	event := mailjetDeliveryEvent{
		Event: "bounce", Time: 1795500000, MessageGUID: "mailjet-guid",
		CustomID: "testagram-0123456789abcdef0123456789abcdef@testagram.site",
		Email: "Person@example.test", HardBounce: true,
	}
	first := mailjetEventFingerprint(event)
	second := mailjetEventFingerprint(event)
	if first == "" || first != second {
		t.Fatalf("event fingerprint is not stable: %q / %q", first, second)
	}
	event.Event = "spam"
	if first == mailjetEventFingerprint(event) {
		t.Fatal("different event types produced the same event fingerprint")
	}
}
