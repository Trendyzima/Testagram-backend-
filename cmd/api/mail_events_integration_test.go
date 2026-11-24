package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

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
	t.Setenv("MAILJET_API_KEY", "")
	t.Setenv("MAILJET_SECRET_KEY", "")
	t.Setenv("AUTH_FROM_EMAIL", "")

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
