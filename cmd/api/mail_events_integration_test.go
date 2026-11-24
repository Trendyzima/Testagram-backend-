package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMailjetDeliveryEventIsIdempotentAndComplaintSuppressesRecipient(t *testing.T) {
	db := nativeRecoveryTestDatabase(t)
	ctx := context.Background()
	t.Setenv("TESTAGRAM_MAIL_ENCRYPTION_KEY", strings.Repeat("4c", 32))
	t.Setenv("MAILJET_API_KEY", "ci-api-key")
	t.Setenv("MAILJET_SECRET_KEY", "ci-secret-key")
	t.Setenv("AUTH_FROM_EMAIL", "no-reply@testagram.site")
	svc := &service{db: db}

	recipient := fmt.Sprintf("mail-event-%d@example.test", time.Now().UnixNano())
	if err := svc.enqueueAuthEmail(ctx, recipient, "CI delivery event", "<p>Test</p>"); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := mailRecipientFingerprint(recipient)
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := db.QueryRow(ctx, "SELECT id FROM testagram_mail.outbox WHERE recipient_fingerprint=$1 ORDER BY id DESC LIMIT 1", fingerprint).Scan(&id); err != nil {
		t.Fatal(err)
	}
	customID := "testagram-ci-mail-message"
	defer func() {
		_, _ = db.Exec(context.Background(), "DELETE FROM testagram_mail.delivery_events WHERE provider_message_id=$1", customID)
		_, _ = db.Exec(context.Background(), "DELETE FROM testagram_mail.recipient_suppressions WHERE recipient_fingerprint=$1", fingerprint)
		_, _ = db.Exec(context.Background(), "DELETE FROM testagram_mail.outbox WHERE id=$1", id)
	}()

	send := func(eventType string, eventTime int64) (*httptest.ResponseRecorder, string) {
		event := mailjetDeliveryEvent{
			Event: eventType, Time: eventTime, MessageGUID: "ci-mailjet-guid",
			MessageID: json.Number("123456"), CustomID: customID, Email: recipient,
			HardBounce: false, ErrorRelatedTo: "recipient",
		}
		payload, err := json.Marshal([]mailjetDeliveryEvent{event})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/internal/mailjet/events", strings.NewReader(string(payload)))
		req.SetBasicAuth("ci-api-key", "ci-secret-key")
		rec := httptest.NewRecorder()
		svc.mailjetDeliveryEvents(rec, req)
		return rec, mailjetEventFingerprint(event)
	}

	firstTime := time.Now().Unix()
	if got, eventID := send("sent", firstTime); got.Code != http.StatusOK {
		t.Fatalf("delivery event status=%d body=%s", got.Code, got.Body.String())
	} else {
		var processed bool
		if err := db.QueryRow(ctx, "SELECT processed_at IS NOT NULL FROM testagram_mail.delivery_events WHERE provider_event_id=$1", eventID).Scan(&processed); err != nil {
			t.Fatal(err)
		}
		if processed {
			t.Fatal("early event was processed before the outbox custom message ID existed")
		}
	}
	if _, err := db.Exec(ctx, "UPDATE testagram_mail.outbox SET provider_message_id=$2,available_at=now()-interval '10 years' WHERE id=$1", id, customID); err != nil {
		t.Fatal(err)
	}
	if err := svc.reconcileMailjetEvents(ctx); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := db.QueryRow(ctx, "SELECT delivery_status FROM testagram_mail.outbox WHERE id=$1", id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "delivered" {
		t.Fatalf("delivery state=%q", state)
	}
	if got, eventID := send("sent", firstTime); got.Code != http.StatusOK {
		t.Fatalf("duplicate event status=%d body=%s", got.Code, got.Body.String())
	} else {
		var count int
		if err := db.QueryRow(ctx, "SELECT count(*) FROM testagram_mail.delivery_events WHERE provider_event_id=$1", eventID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("duplicate event rows=%d", count)
		}
	}

	if got, _ := send("spam", firstTime+1); got.Code != http.StatusOK {
		t.Fatalf("complaint event status=%d body=%s", got.Code, got.Body.String())
	}
	if err := db.QueryRow(ctx, "SELECT delivery_status FROM testagram_mail.outbox WHERE id=$1", id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "complained" {
		t.Fatalf("complaint state=%q", state)
	}

	worked, err := svc.processOneMail(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !worked {
		t.Fatal("worker did not consume suppressed queued message")
	}
	var status, code string
	if err := db.QueryRow(ctx, "SELECT status,last_error_code FROM testagram_mail.outbox WHERE id=$1", id).Scan(&status, &code); err != nil {
		t.Fatal(err)
	}
	if status != "dead" || code != "RECIPIENT_SUPPRESSED" {
		t.Fatalf("suppressed state=(%s,%s)", status, code)
	}
}
