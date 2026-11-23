package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func configureMailWorkerTest(t *testing.T) {
	t.Helper()
	t.Setenv("TESTAGRAM_MAIL_ENCRYPTION_KEY", strings.Repeat("61", 32))
	t.Setenv("CLOUDFLARE_EMAIL_ACCOUNT_ID", "0123456789abcdef0123456789abcdef")
	t.Setenv("CLOUDFLARE_EMAIL_API_TOKEN", "ci-provider-token")
	t.Setenv("AUTH_FROM_EMAIL", "no-reply@testagram.site")
}

func TestMailWorkerRetriesDefiniteFailureAndPreservesPayload(t *testing.T) {
	db := nativeRecoveryTestDatabase(t)
	ctx := context.Background()
	configureMailWorkerTest(t)
	recipient := fmt.Sprintf("mail-retry-%d@example.test", time.Now().UnixNano())
	if err := enqueueAuthEmailWith(ctx, db, recipient, "Retry test", "<p>Retry safely</p>"); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := db.QueryRow(ctx, "SELECT id FROM testagram_mail.outbox WHERE recipient_fingerprint=$1 ORDER BY id DESC LIMIT 1", mustMailFingerprint(t, recipient)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = db.Exec(context.Background(), "DELETE FROM testagram_mail.outbox WHERE id=$1", id) }()

	if _, err := db.Exec(ctx, "UPDATE testagram_mail.outbox SET available_at=now()-interval '10 years' WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	calls := 0
	svc := &service{db: db, mailSender: func(context.Context, queuedMail) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("HTTP 429 temporary rate limit")
		}
		return "cf-retry-success", nil
	}}
	worked, err := svc.processOneMail(ctx)
	if err != nil || !worked {
		t.Fatalf("first worker pass worked=%v err=%v", worked, err)
	}
	var status, errorCode string
	var attempts int
	var ciphertext []byte
	if err := db.QueryRow(ctx, "SELECT status,last_error_code,attempts,payload_ciphertext FROM testagram_mail.outbox WHERE id=$1", id).Scan(&status, &errorCode, &attempts, &ciphertext); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || errorCode != "CLOUDFLARE_DELIVERY_FAILED" || attempts != 1 || len(ciphertext) == 0 {
		t.Fatalf("retry state=(%s,%s,%d,payload=%d)", status, errorCode, attempts, len(ciphertext))
	}
	if _, err := db.Exec(ctx, "UPDATE testagram_mail.outbox SET available_at=now()-interval '10 years' WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	worked, err = svc.processOneMail(ctx)
	if err != nil || !worked {
		t.Fatalf("second worker pass worked=%v err=%v", worked, err)
	}
	if err := db.QueryRow(ctx, "SELECT status,attempts,provider_message_id FROM testagram_mail.outbox WHERE id=$1", id).Scan(&status, &attempts, &errorCode); err != nil {
		t.Fatal(err)
	}
	if status != "sent" || attempts != 2 || errorCode != "cf-retry-success" || calls != 2 {
		t.Fatalf("successful retry state=(%s,%d,%s), calls=%d", status, attempts, errorCode, calls)
	}
}

func TestMailWorkerQuarantinesAmbiguousOutcomeWithoutResending(t *testing.T) {
	db := nativeRecoveryTestDatabase(t)
	ctx := context.Background()
	configureMailWorkerTest(t)
	recipient := fmt.Sprintf("mail-uncertain-%d@example.test", time.Now().UnixNano())
	if err := enqueueAuthEmailWith(ctx, db, recipient, "Ambiguous test", "<p>Do not duplicate</p>"); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := db.QueryRow(ctx, "SELECT id FROM testagram_mail.outbox WHERE recipient_fingerprint=$1 ORDER BY id DESC LIMIT 1", mustMailFingerprint(t, recipient)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = db.Exec(context.Background(), "DELETE FROM testagram_mail.outbox WHERE id=$1", id) }()
	if _, err := db.Exec(ctx, "UPDATE testagram_mail.outbox SET available_at=now()-interval '10 years' WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	calls := 0
	svc := &service{db: db, mailSender: func(context.Context, queuedMail) (string, error) {
		calls++
		return "", ambiguousMailError{cause: errors.New("provider request timed out")}
	}}
	worked, err := svc.processOneMail(ctx)
	if err != nil || !worked {
		t.Fatalf("worker pass worked=%v err=%v", worked, err)
	}
	var status, code string
	var attempts int
	var ciphertext []byte
	if err := db.QueryRow(ctx, "SELECT status,last_error_code,attempts,payload_ciphertext FROM testagram_mail.outbox WHERE id=$1", id).Scan(&status, &code, &attempts, &ciphertext); err != nil {
		t.Fatal(err)
	}
	if status != "uncertain" || code != "CLOUDFLARE_OUTCOME_UNKNOWN" || attempts != 1 || len(ciphertext) == 0 {
		t.Fatalf("uncertain state=(%s,%s,%d,payload=%d)", status, code, attempts, len(ciphertext))
	}
	worked, err = svc.processOneMail(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if worked || calls != 1 {
		t.Fatalf("uncertain mail was automatically resent: worked=%v provider_calls=%d", worked, calls)
	}
}
