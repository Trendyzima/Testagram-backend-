package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestMailOutboxPersistsEncryptedPayload(t *testing.T) {
	db := nativeRecoveryTestDatabase(t)
	ctx := context.Background()
	t.Setenv("TESTAGRAM_MAIL_ENCRYPTION_KEY", strings.Repeat("cd", 32))

	svc := &service{db: db}
	recipient := fmt.Sprintf("mail-outbox-%d@example.test", time.Now().UnixNano())
	const secretLink = "https://testagram.example/reset-password?token=one-time-secret-token"
	if err := svc.enqueueAuthEmail(ctx, recipient, "Testagram security notice", "<a href=\""+secretLink+"\">Continue</a>"); err != nil {
		t.Fatal(err)
	}

	var id int64
	var ciphertext, nonce []byte
	var status string
	var preferenceUserID, preferenceCategory string
	if err := db.QueryRow(ctx, `
		SELECT id,payload_ciphertext,payload_nonce,status,COALESCE(preference_user_id::text,''),COALESCE(preference_category,'')
		FROM testagram_mail.outbox
		WHERE payload_ciphertext IS NOT NULL
		ORDER BY id DESC LIMIT 1
	`).Scan(&id, &ciphertext, &nonce, &status, &preferenceUserID, &preferenceCategory); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = db.Exec(context.Background(), "DELETE FROM testagram_mail.outbox WHERE id=$1", id) }()

	if status != "pending" {
		t.Fatalf("new mail status = %q, want pending", status)
	}
	if preferenceUserID != "" || preferenceCategory != "" {
		t.Fatal("transactional account email must not be tagged with social consent metadata")
	}
	if bytes.Contains(ciphertext, []byte(secretLink)) || bytes.Contains(ciphertext, []byte(recipient)) {
		t.Fatal("mail recipient or one-time link was stored in plaintext")
	}
	aead, err := mailCipher()
	if err != nil {
		t.Fatal(err)
	}
	plain, err := aead.Open(nil, nonce, ciphertext, []byte("testagram-mail-v1"))
	if err != nil {
		t.Fatal(err)
	}
	var message queuedMail
	if err := json.Unmarshal(plain, &message); err != nil {
		t.Fatal(err)
	}
	if message.Recipient != recipient || message.Subject != "Testagram security notice" || !strings.Contains(message.HTML, secretLink) {
		t.Fatalf("decrypted outbox message did not match queued content: %#v", message)
	}
}

func TestSocialMailOutboxCarriesConsentCategory(t *testing.T) {
	db := nativeRecoveryTestDatabase(t)
	ctx := context.Background()
	t.Setenv("TESTAGRAM_MAIL_ENCRYPTION_KEY", strings.Repeat("ab", 32))

	recipient := fmt.Sprintf("social-mail-%d@example.test", time.Now().UnixNano())
	userID := "123e4567-e89b-42d3-a456-426614174199"
	if err := enqueueSocialEmailWith(ctx, db, recipient, userID, "new_post",
		"A person you follow shared a new Testagram post", "<p>New post</p>"); err != nil {
		t.Fatal(err)
	}

	var id int64
	var storedUserID, category string
	if err := db.QueryRow(ctx, `
		SELECT id,preference_user_id::text,preference_category
		FROM testagram_mail.outbox
		WHERE preference_user_id=$1
		ORDER BY id DESC LIMIT 1
	`, userID).Scan(&id, &storedUserID, &category); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = db.Exec(context.Background(), "DELETE FROM testagram_mail.outbox WHERE id=$1", id) }()
	if storedUserID != userID || category != "new_post" {
		t.Fatalf("social mail consent reference = (%q,%q), want (%q,new_post)", storedUserID, category, userID)
	}
}

func TestMailOutboxInsertRollsBackWithCallerTransaction(t *testing.T) {
	db := nativeRecoveryTestDatabase(t)
	ctx := context.Background()
	t.Setenv("TESTAGRAM_MAIL_ENCRYPTION_KEY", strings.Repeat("ef", 32))

	var before int64
	if err := db.QueryRow(ctx, "SELECT count(*) FROM testagram_mail.outbox").Scan(&before); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := enqueueAuthEmailWith(ctx, tx, "atomic-mail@example.test", "Atomic queue test", "<p>transactional</p>"); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	var inside int64
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM testagram_mail.outbox").Scan(&inside); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if inside != before+1 {
		_ = tx.Rollback(ctx)
		t.Fatalf("outbox rows inside transaction = %d, want %d", inside, before+1)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var after int64
	if err := db.QueryRow(ctx, "SELECT count(*) FROM testagram_mail.outbox").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("outbox rows after rollback = %d, want %d", after, before)
	}
}
