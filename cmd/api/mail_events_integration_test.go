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

func TestCloudflareDeliveryEventIsIdempotentAndComplaintSuppressesRecipient(t *testing.T) {
	db := nativeRecoveryTestDatabase(t)
	ctx := context.Background()
	t.Setenv("TESTAGRAM_MAIL_ENCRYPTION_KEY", strings.Repeat("4c", 32))
	t.Setenv("TESTAGRAM_CLOUDFLARE_EVENTS_TOKEN", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	t.Setenv("CLOUDFLARE_EMAIL_ACCOUNT_ID", "0123456789abcdef0123456789abcdef")
	t.Setenv("CLOUDFLARE_EMAIL_API_TOKEN", "ci-provider-token")
	t.Setenv("AUTH_FROM_EMAIL", "no-reply@testagram.site")
	svc := &service{db: db}

	recipient := fmt.Sprintf("mail-event-%d@example.test", time.Now().UnixNano())
	if err := svc.enqueueAuthEmail(ctx, recipient, "CI delivery event", "<p>Test</p>"); err != nil { t.Fatal(err) }
	fingerprint, err := mailRecipientFingerprint(recipient)
	if err != nil { t.Fatal(err) }
	var id int64
	if err := db.QueryRow(ctx, "SELECT id FROM testagram_mail.outbox WHERE recipient_fingerprint=$1 ORDER BY id DESC LIMIT 1", fingerprint).Scan(&id); err != nil { t.Fatal(err) }
	defer func() {
		_, _ = db.Exec(context.Background(), "DELETE FROM testagram_mail.delivery_events WHERE provider_message_id='ci-mail-message'")
		_, _ = db.Exec(context.Background(), "DELETE FROM testagram_mail.recipient_suppressions WHERE recipient_fingerprint=$1", fingerprint)
		_, _ = db.Exec(context.Background(), "DELETE FROM testagram_mail.outbox WHERE id=$1", id)
	}()
	if _, err := db.Exec(ctx, "UPDATE testagram_mail.outbox SET provider_message_id='ci-mail-message' WHERE id=$1", id); err != nil { t.Fatal(err) }

	send := func(eventID, kind string) *httptest.ResponseRecorder {
		payload, _ := json.Marshal(map[string]any{
			"type": kind,
			"source": map[string]any{"type":"email.sending","domain":"testagram.site"},
			"payload": map[string]any{
				"eventId":eventID,"messageId":"ci-mail-message","recipient":recipient,
				"terminal":true,"delivery":map[string]any{"status":"delivered","smtpStatusCode":"250"},
			},
			"metadata":map[string]any{"eventSchemaVersion":1},
		})
		req := httptest.NewRequest(http.MethodPost, "/internal/mail/events", strings.NewReader(string(payload)))
		req.Header.Set("Authorization", "Bearer ci-event-token")
		rec := httptest.NewRecorder()
		svc.cloudflareMailEvent(rec, req)
		return rec
	}
	if got := send("ci-event-1","cf.email.sending.message.delivered"); got.Code != http.StatusAccepted { t.Fatalf("delivery event status=%d body=%s",got.Code,got.Body.String()) }
	var state string
	if err := db.QueryRow(ctx,"SELECT delivery_status FROM testagram_mail.outbox WHERE id=$1",id).Scan(&state); err != nil { t.Fatal(err) }
	if state!="delivered" { t.Fatalf("delivery state=%q",state) }
	if got := send("ci-event-1","cf.email.sending.message.delivered"); got.Code != http.StatusAccepted { t.Fatalf("duplicate event status=%d",got.Code) }
	var count int
	if err := db.QueryRow(ctx,"SELECT count(*) FROM testagram_mail.delivery_events WHERE provider_event_id='ci-event-1'").Scan(&count); err != nil { t.Fatal(err) }
	if count!=1 { t.Fatalf("duplicate event rows=%d",count) }

	if got := send("ci-event-2","cf.email.sending.message.complained"); got.Code != http.StatusAccepted { t.Fatalf("complaint event status=%d body=%s",got.Code,got.Body.String()) }
	if err := db.QueryRow(ctx,"SELECT delivery_status FROM testagram_mail.outbox WHERE id=$1",id).Scan(&state); err != nil { t.Fatal(err) }
	if state!="complained" { t.Fatalf("complaint state=%q",state) }

	worked, err := svc.processOneMail(ctx)
	if err != nil { t.Fatal(err) }
	if !worked { t.Fatal("worker did not consume suppressed queued message") }
	var status, code string
	if err := db.QueryRow(ctx,"SELECT status,last_error_code FROM testagram_mail.outbox WHERE id=$1",id).Scan(&status,&code); err != nil { t.Fatal(err) }
	if status!="dead" || code!="RECIPIENT_SUPPRESSED" { t.Fatalf("suppressed state=(%s,%s)",status,code) }
}
