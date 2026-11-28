package main

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestMailWorkerNeverSendsPendingSocialMailAfterConsentWithdrawal(t *testing.T) {
	db := nativeRecoveryTestDatabase(t)
	ctx := context.Background()
	configureMailWorkerTest(t)
	// The shared isolated integration database may contain pending messages from
	// earlier worker cases encrypted with a different fixture key. Keep those
	// rows out of this worker pass so the test exercises only its own message.
	if _, err := db.Exec(ctx, "UPDATE testagram_mail.outbox SET available_at=now()+interval '1 day' WHERE status='pending'"); err != nil {
		t.Fatal(err)
	}
	userID := "123e4567-e89b-42d3-a456-426614174198"
	username := fmt.Sprintf("mailwithdraw%d", time.Now().UnixNano()%1000000)
	recipient := username + "@example.test"
	if _, err := db.Exec(ctx, "INSERT INTO testagram_local.accounts(user_id,username,display_name) VALUES($1::uuid,$2,'Mail Withdrawal Test')", userID, username); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = db.Exec(context.Background(), "DELETE FROM testagram_mail.outbox WHERE preference_user_id=$1::uuid", userID)
		_, _ = db.Exec(context.Background(), "DELETE FROM testagram_mail.social_unsubscribe_tokens WHERE user_id=$1::uuid", userID)
		_, _ = db.Exec(context.Background(), "DELETE FROM testagram_local.email_notification_preferences WHERE user_id=$1::uuid", userID)
		_, _ = db.Exec(context.Background(), "DELETE FROM testagram_local.accounts WHERE user_id=$1::uuid", userID)
	}()

	enabled := true
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := upsertSocialEmailPreferences(ctx, tx, userID, socialEmailPreferenceInput{NewPostEmailEnabled: &enabled}); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := enqueueSocialEmailWith(ctx, db, recipient, userID, "new_post", "A followed account posted", "<p>Post</p>"); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := db.QueryRow(ctx, "SELECT id FROM testagram_mail.outbox WHERE preference_user_id=$1::uuid AND preference_category='new_post' ORDER BY id DESC LIMIT 1", userID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		UPDATE testagram_local.email_notification_preferences
		SET new_post_email_enabled=FALSE,new_post_consent_withdrawn_at=now()
		WHERE user_id=$1::uuid
	`, userID); err != nil {
		t.Fatal(err)
	}

	calls := 0
	svc := &service{db: db, mailSender: func(context.Context, queuedMail) (string, error) {
		calls++
		return "must-not-send", nil
	}}
	worked, err := svc.processOneMail(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if worked || calls != 0 {
		t.Fatalf("worker sent withdrawn social email: worked=%v provider_calls=%d", worked, calls)
	}
	var status, code string
	var payloadLength int
	if err := db.QueryRow(ctx, "SELECT status,last_error_code,octet_length(payload_ciphertext) FROM testagram_mail.outbox WHERE id=$1", id).Scan(&status, &code, &payloadLength); err != nil {
		t.Fatal(err)
	}
	if status != "dead" || code != "CONSENT_WITHDRAWN" || payloadLength != 0 {
		t.Fatalf("withdrawn outbox state=(%s,%s,payload=%d)", status, code, payloadLength)
	}
}
