package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestNativeBounceReportCorrelatesAndDeduplicatesHardBounce(t *testing.T) {
	db := nativeRecoveryTestDatabase(t)
	ctx := context.Background()
	t.Setenv("TESTAGRAM_MAIL_ENCRYPTION_KEY", strings.Repeat("64", 32))
	t.Setenv("TESTAGRAM_MAIL_VERP_KEY", strings.Repeat("65", 32))

	recipient := fmt.Sprintf("native-bounce-%d@example.test", time.Now().UnixNano())
	if err := enqueueAuthEmailWith(ctx, db, recipient, "Bounce integration test", "<p>Test</p>"); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := mailRecipientFingerprint(recipient)
	if err != nil {
		t.Fatal(err)
	}
	var outboxID int64
	if err := db.QueryRow(ctx, "SELECT id FROM testagram_mail.outbox WHERE recipient_fingerprint=$1 ORDER BY id DESC LIMIT 1", fingerprint).Scan(&outboxID); err != nil {
		t.Fatal(err)
	}
	returnPath, tokenHash, err := nativeMailReturnPath(outboxID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = db.Exec(context.Background(), "DELETE FROM testagram_mail.recipient_suppressions WHERE recipient_fingerprint=$1", fingerprint)
		_, _ = db.Exec(context.Background(), "DELETE FROM testagram_mail.delivery_events WHERE provider_message_id=$1", fmt.Sprintf("native-outbox-%d", outboxID))
		_, _ = db.Exec(context.Background(), "DELETE FROM testagram_mail.inbound_reports WHERE token_hash=$1", tokenHash)
		_, _ = db.Exec(context.Background(), "DELETE FROM testagram_mail.outbox WHERE id=$1", outboxID)
	}()

	if _, err := db.Exec(ctx, `
		INSERT INTO testagram_mail.return_path_tokens(token_hash,outbox_id)
		VALUES($1,$2)
		ON CONFLICT(outbox_id) DO NOTHING
	`, tokenHash, outboxID); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(returnPath, "b+") {
		t.Fatalf("invalid generated VERP address: %q", returnPath)
	}

	raw := []byte("Message-ID: <native-bounce-fixture@example.test>\\r\\n" +
		"Final-Recipient: rfc822; " + recipient + "\\r\\n" +
		"Action: failed\\r\\nStatus: 5.1.1\\r\\n")
	report := nativeBounceReport{
		Recipient: recipient, EventType: "dsn.bounced", Status: "5.1.1",
		Diagnostic: "smtp; 550 5.1.1 user unknown", RemoteMTA: "dns; mx.example.test",
	}
	if err := processNativeBounceReport(ctx, db, tokenHash, raw, report); err != nil {
		t.Fatal(err)
	}
	if err := processNativeBounceReport(ctx, db, tokenHash, raw, report); err != nil {
		t.Fatalf("replayed DSN was not handled idempotently: %v", err)
	}

	var deliveryStatus string
	var hasFailedAt bool
	if err := db.QueryRow(ctx, "SELECT delivery_status,delivery_failed_at IS NOT NULL FROM testagram_mail.outbox WHERE id=$1", outboxID).Scan(&deliveryStatus, &hasFailedAt); err != nil {
		t.Fatal(err)
	}
	if deliveryStatus != "bounced" || !hasFailedAt {
		t.Fatalf("hard-bounce delivery state=(%s,failed_at=%v)", deliveryStatus, hasFailedAt)
	}
	var suppressionCount, reportCount, eventCount int64
	if err := db.QueryRow(ctx, "SELECT count(*) FROM testagram_mail.recipient_suppressions WHERE recipient_fingerprint=$1 AND reason='hard_bounce'", fingerprint).Scan(&suppressionCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "SELECT count(*) FROM testagram_mail.inbound_reports WHERE token_hash=$1", tokenHash).Scan(&reportCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "SELECT count(*) FROM testagram_mail.delivery_events WHERE provider_message_id=$1 AND event_type='dsn.bounced'", fmt.Sprintf("native-outbox-%d", outboxID)).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if suppressionCount != 1 || reportCount != 1 || eventCount != 1 {
		t.Fatalf("idempotency counts: suppressions=%d reports=%d events=%d", suppressionCount, reportCount, eventCount)
	}

	mismatched := report
	mismatched.Recipient = "different-recipient@example.test"
	if err := processNativeBounceReport(ctx, db, tokenHash, []byte("forged recipient report"), mismatched); err == nil {
		t.Fatal("report for a different recipient was correlated to this VERP token")
	}
}

func TestNativeMailWorkerSuppressesPreviouslyComplainedRecipient(t *testing.T) {
	db := nativeRecoveryTestDatabase(t)
	ctx := context.Background()
	t.Setenv("TESTAGRAM_MAIL_ENCRYPTION_KEY", strings.Repeat("4c", 32))
	t.Setenv("TESTAGRAM_MAIL_VERP_KEY", strings.Repeat("4d", 32))
	_, dkimPath := writeTestDKIMKey(t, 0600)
	t.Setenv("TESTAGRAM_DKIM_PRIVATE_KEY_FILE", dkimPath)
	t.Setenv("TESTAGRAM_DKIM_SELECTOR", "test2026")
	t.Setenv("TESTAGRAM_MAIL_VERP_KEY", strings.Repeat("63", 32))
	_, keyPath := writeTestDKIMKey(t, 0600)
	t.Setenv("TESTAGRAM_DKIM_PRIVATE_KEY_FILE", keyPath)

	svc := &service{db: db}
	recipient := fmt.Sprintf("native-mail-suppression-%d@example.test", time.Now().UnixNano())
	if err := svc.enqueueAuthEmail(ctx, recipient, "Native mail suppression test", "<p>Test</p>"); err != nil {
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
	defer func() {
		_, _ = db.Exec(context.Background(), "DELETE FROM testagram_mail.recipient_suppressions WHERE recipient_fingerprint=$1", fingerprint)
		_, _ = db.Exec(context.Background(), "DELETE FROM testagram_mail.outbox WHERE id=$1", id)
	}()

	if _, err := db.Exec(ctx, `
		INSERT INTO testagram_mail.recipient_suppressions
			(recipient_fingerprint,reason,source_event_id)
		VALUES ($1,'complaint',$2)
		ON CONFLICT (recipient_fingerprint) DO NOTHING
	`, fingerprint, "testagram-native-complaint-"+fmt.Sprint(id)); err != nil {
		t.Fatal(err)
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
		t.Fatalf("suppressed message state=(%s,%s)", status, code)
	}
}
