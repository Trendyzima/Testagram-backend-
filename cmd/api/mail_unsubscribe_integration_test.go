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

func TestSocialEmailUnsubscribeGetIsSafeAndPostWithdrawsConsent(t *testing.T) {
	db := nativeRecoveryTestDatabase(t)
	ctx := context.Background()
	t.Setenv("TESTAGRAM_MAIL_ENCRYPTION_KEY", strings.Repeat("5d", 32))
	t.Setenv("AUTH_PUBLIC_BASE_URL", "https://testagram.site")
	userID := "123e4567-e89b-42d3-a456-426614174199"
	username := fmt.Sprintf("unsub%d", time.Now().UnixNano()%1000000)
	recipient := username + "@example.test"
	_, err := db.Exec(ctx, "INSERT INTO testagram_local.accounts(user_id,username,display_name) VALUES($1::uuid,$2,'Unsubscribe Test')", userID, username)
	if err != nil {
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
	if err := enqueueSocialEmailWith(ctx, db, recipient, userID, "new_post", "A followed account posted", "<p>A new post</p>"); err != nil {
		t.Fatal(err)
	}

	var ciphertext, nonce []byte
	var outboxID int64
	if err := db.QueryRow(ctx, "SELECT id,payload_ciphertext,payload_nonce FROM testagram_mail.outbox WHERE preference_user_id=$1::uuid AND preference_category='new_post' ORDER BY id DESC LIMIT 1", userID).Scan(&outboxID, &ciphertext, &nonce); err != nil {
		t.Fatal(err)
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
	marker := "/api/email/unsubscribe?token="
	start := strings.Index(message.HTML, marker)
	if start < 0 {
		t.Fatal("social email has no unsubscribe link")
	}
	token := strings.SplitN(message.HTML[start+len(marker):], "\"", 2)[0]
	if token == "" {
		t.Fatal("unsubscribe token is empty")
	}

	svc := &service{db: db}
	get := httptest.NewRequest(http.MethodGet, "/api/email/unsubscribe?token="+token, nil)
	getResponse := httptest.NewRecorder()
	svc.socialEmailUnsubscribe(getResponse, get)
	if getResponse.Code != http.StatusOK {
		t.Fatalf("unsubscribe confirmation GET status=%d", getResponse.Code)
	}
	var enabledAfterGet bool
	if err := db.QueryRow(ctx, "SELECT new_post_email_enabled FROM testagram_local.email_notification_preferences WHERE user_id=$1::uuid", userID).Scan(&enabledAfterGet); err != nil {
		t.Fatal(err)
	}
	if !enabledAfterGet {
		t.Fatal("GET request unexpectedly withdrew email consent")
	}

	post := httptest.NewRequest(http.MethodPost, "/api/email/unsubscribe?token="+token, strings.NewReader("List-Unsubscribe=One-Click"))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postResponse := httptest.NewRecorder()
	svc.socialEmailUnsubscribe(postResponse, post)
	if postResponse.Code != http.StatusOK {
		t.Fatalf("unsubscribe POST status=%d body=%s", postResponse.Code, postResponse.Body.String())
	}

	var enabledAfterPost bool
	var pending int
	var used bool
	if err := db.QueryRow(ctx, "SELECT new_post_email_enabled FROM testagram_local.email_notification_preferences WHERE user_id=$1::uuid", userID).Scan(&enabledAfterPost); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "SELECT count(*) FROM testagram_mail.outbox WHERE preference_user_id=$1::uuid AND status='pending'", userID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "SELECT used_at IS NOT NULL FROM testagram_mail.social_unsubscribe_tokens WHERE user_id=$1::uuid", userID).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if enabledAfterPost || pending != 0 || !used {
		t.Fatalf("unsubscribe state enabled=%v pending=%d token_used=%v", enabledAfterPost, pending, used)
	}
}
