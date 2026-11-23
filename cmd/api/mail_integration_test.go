package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	if _, err := db.Exec(ctx, `
		INSERT INTO testagram_local.accounts(user_id,username,display_name)
		VALUES($1::uuid,'socialmailconsent','Social Mail Consent')
		ON CONFLICT(user_id) DO UPDATE SET account_status='active'
	`, userID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = db.Exec(context.Background(), "DELETE FROM testagram_mail.outbox WHERE preference_user_id=$1::uuid", userID)
		_, _ = db.Exec(context.Background(), "DELETE FROM testagram_mail.social_unsubscribe_tokens WHERE user_id=$1::uuid", userID)
		_, _ = db.Exec(context.Background(), "DELETE FROM testagram_local.accounts WHERE user_id=$1::uuid", userID)
	}()
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

func TestSocialMailConsentIsRecheckedAgainstCurrentPreference(t *testing.T) {
	db := nativeRecoveryTestDatabase(t)
	ctx := context.Background()
	userID := "123e4567-e89b-42d3-a456-426614174188"
	_, err := db.Exec(ctx, `
		INSERT INTO testagram_local.accounts(user_id,username,display_name)
		VALUES($1::uuid,'mailconsenttest','Mail Consent Test')
		ON CONFLICT(user_id) DO UPDATE SET account_status='active'
	`, userID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = db.Exec(context.Background(), "DELETE FROM testagram_local.accounts WHERE user_id=$1::uuid", userID)
	}()
	_, err = db.Exec(ctx, `
		INSERT INTO testagram_local.email_notification_preferences(
			user_id,new_follower_email_enabled,new_post_email_enabled
		) VALUES($1::uuid,FALSE,FALSE)
		ON CONFLICT(user_id) DO UPDATE SET
			new_follower_email_enabled=FALSE,new_post_email_enabled=FALSE,
			new_follower_consent_at=NULL,new_follower_consent_withdrawn_at=NULL,
			new_post_consent_at=NULL,new_post_consent_withdrawn_at=NULL
	`, userID)
	if err != nil {
		t.Fatal(err)
	}
	enabled, disabled := true, false
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := upsertSocialEmailPreferences(ctx, tx, userID, socialEmailPreferenceInput{NewFollowerEmailEnabled: &enabled, NewPostEmailEnabled: &disabled}); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("first-time partial opt-in failed: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	active, err := socialMailConsentActive(ctx, db, userID, "new_follower")
	if err != nil || !active {
		t.Fatalf("enabled follower email consent = %v, err=%v; want true", active, err)
	}
	active, err = socialMailConsentActive(ctx, db, userID, "new_post")
	if err != nil || active {
		t.Fatalf("disabled post email consent = %v, err=%v; want false", active, err)
	}
	_, err = db.Exec(ctx, `
		UPDATE testagram_local.email_notification_preferences
		SET new_follower_email_enabled=FALSE,new_follower_consent_withdrawn_at=now()
		WHERE user_id=$1::uuid
	`, userID)
	if err != nil {
		t.Fatal(err)
	}
	active, err = socialMailConsentActive(ctx, db, userID, "new_follower")
	if err != nil || active {
		t.Fatalf("withdrawn follower email consent = %v, err=%v; want false", active, err)
	}
}

func TestNativeSignupQueuesEmailOTPAndOTPVerifiesAccount(t *testing.T) {
	db := nativeRecoveryTestDatabase(t)
	ctx := context.Background()
	t.Setenv("TESTAGRAM_MAIL_ENCRYPTION_KEY", strings.Repeat("9a", 32))
	t.Setenv("TESTAGRAM_AUTH_JWT_SECRET", strings.Repeat("native-auth-test-key-", 4))

	stamp := time.Now().UnixNano()
	email := fmt.Sprintf("native-otp-%d@example.test", stamp)
	username := fmt.Sprintf("otp%d", stamp)
	ip := fmt.Sprintf("198.51.100.%d:4321", stamp%200+1)
	svc := &service{db: db}

	signupBody, err := json.Marshal(authInput{
		Email: email, Username: username, Password: "correct-horse-battery-staple-2026",
	})
	if err != nil {
		t.Fatal(err)
	}
	signupRequest := httptest.NewRequest(http.MethodPost, "/api/auth/signup", strings.NewReader(string(signupBody)))
	signupRequest.RemoteAddr = ip
	signupResponse := httptest.NewRecorder()
	svc.authRespond(signupResponse, signupRequest, "signup")
	if signupResponse.Code != http.StatusAccepted {
		t.Fatalf("signup status = %d, body=%s; want 202", signupResponse.Code, signupResponse.Body.String())
	}

	var userID string
	var storedOTPHash []byte
	if err := db.QueryRow(ctx, `
		SELECT user_id::text,verification_otp_hash
		FROM testagram_auth.identities WHERE email_normalized=$1
	`, email).Scan(&userID, &storedOTPHash); err != nil {
		t.Fatal(err)
	}
	var outboxID int64
	var ciphertext, nonce []byte
	if err := db.QueryRow(ctx, `
		SELECT id,payload_ciphertext,payload_nonce
		FROM testagram_mail.outbox
		WHERE status='pending' ORDER BY id DESC LIMIT 1
	`).Scan(&outboxID, &ciphertext, &nonce); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = db.Exec(context.Background(), "DELETE FROM testagram_local.accounts WHERE user_id=$1::uuid", userID)
		_, _ = db.Exec(context.Background(), "DELETE FROM testagram_auth.identities WHERE user_id=$1::uuid", userID)
		_, _ = db.Exec(context.Background(), "DELETE FROM testagram_mail.outbox WHERE id=$1", outboxID)
	}()

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
	if message.Recipient != email || !strings.Contains(message.Subject, "verification code") {
		t.Fatalf("signup mail recipient/subject = %q / %q", message.Recipient, message.Subject)
	}
	const otpMarker = `letter-spacing:8px">`
	parts := strings.SplitN(message.HTML, otpMarker, 2)
	if len(parts) != 2 {
		t.Fatal("verification email did not contain the formatted one-time code")
	}
	otp := strings.SplitN(parts[1], "</p>", 2)[0]
	if len(otp) != 6 {
		t.Fatalf("email OTP length = %d, want six digits", len(otp))
	}
	for _, digit := range otp {
		if digit < '0' || digit > '9' {
			t.Fatalf("email OTP contains non-digit %q", digit)
		}
	}
	otpHash, err := authOTPHash(otp)
	if err != nil {
		t.Fatal(err)
	}
	if !hmac.Equal(otpHash, storedOTPHash) {
		t.Fatal("email OTP does not match the stored one-way OTP hash")
	}

	verifyBody, err := json.Marshal(authInput{Email: email, Token: otp})
	if err != nil {
		t.Fatal(err)
	}
	verifyRequest := httptest.NewRequest(http.MethodPost, "/api/auth/verify-email", strings.NewReader(string(verifyBody)))
	verifyRequest.RemoteAddr = ip
	verifyResponse := httptest.NewRecorder()
	svc.authRespond(verifyResponse, verifyRequest, "verify-email")
	if verifyResponse.Code != http.StatusOK {
		t.Fatalf("OTP verification status = %d, body=%s; want 200", verifyResponse.Code, verifyResponse.Body.String())
	}
	var verified bool
	if err := db.QueryRow(ctx, `
		SELECT email_verified_at IS NOT NULL
		FROM testagram_auth.identities WHERE user_id=$1::uuid
	`, userID).Scan(&verified); err != nil {
		t.Fatal(err)
	}
	if !verified {
		t.Fatal("successful OTP verification did not verify the native account email")
	}
	var profileExists bool
	if err := db.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM testagram_local.accounts WHERE user_id=$1::uuid)
	`, userID).Scan(&profileExists); err != nil {
		t.Fatal(err)
	}
	if !profileExists {
		t.Fatal("successful OTP verification did not create the Testagram profile")
	}
}

func TestSocialFollowAndPostEmailsOnlyQueueForConsentingFollowers(t *testing.T) {
	t.Setenv("TESTAGRAM_MAIL_ENCRYPTION_KEY", strings.Repeat("7b", 32))
	db := nativeRecoveryTestDatabase(t)
	ctx := context.Background()
	actorID := "123e4567-e89b-42d3-a456-426614174181"
	consentingID := "123e4567-e89b-42d3-a456-426614174182"
	declinedID := "123e4567-e89b-42d3-a456-426614174183"

	cleanup := func() {
		_, _ = db.Exec(context.Background(), "DELETE FROM testagram_mail.outbox WHERE preference_user_id IN ($1::uuid,$2::uuid)", consentingID, declinedID)
		_, _ = db.Exec(context.Background(), "DELETE FROM testagram_local.email_notification_preferences WHERE user_id IN ($1::uuid,$2::uuid)", consentingID, declinedID)
		_, _ = db.Exec(context.Background(), "DELETE FROM testagram_local.follows WHERE follower_id IN ($1::uuid,$2::uuid,$3::uuid) OR following_id IN ($1::uuid,$2::uuid,$3::uuid)", actorID, consentingID, declinedID)
		_, _ = db.Exec(context.Background(), "DELETE FROM testagram_auth.identities WHERE user_id IN ($1::uuid,$2::uuid)", consentingID, declinedID)
		_, _ = db.Exec(context.Background(), "DELETE FROM testagram_local.accounts WHERE user_id IN ($1::uuid,$2::uuid,$3::uuid)", actorID, consentingID, declinedID)
	}
	cleanup()
	defer cleanup()

	_, err := db.Exec(ctx, `
		INSERT INTO testagram_local.accounts(user_id,username,display_name)
		VALUES
		  ($1::uuid,'mailactor181','Mail Actor'),
		  ($2::uuid,'mailfollower182','Consenting Follower'),
		  ($3::uuid,'mailfollower183','Declined Follower')
	`, actorID, consentingID, declinedID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(ctx, `
		INSERT INTO testagram_auth.identities(user_id,email,username,password_hash,email_verified_at)
		VALUES
		  ($1::uuid,'consenting-follower@example.test','mailfollower182','test-hash',now()),
		  ($2::uuid,'declined-follower@example.test','mailfollower183','test-hash',now())
	`, consentingID, declinedID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(ctx, `
		INSERT INTO testagram_local.follows(follower_id,following_id,status)
		VALUES ($1::uuid,$2::uuid,'accepted'),($3::uuid,$2::uuid,'accepted')
	`, consentingID, actorID, declinedID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(ctx, `
		INSERT INTO testagram_local.email_notification_preferences(
			user_id,new_follower_email_enabled,new_post_email_enabled,
			new_follower_consent_at,new_post_consent_at
		) VALUES ($1::uuid,TRUE,TRUE,now(),now())
	`, consentingID)
	if err != nil {
		t.Fatal(err)
	}

	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := enqueueFollowActivityEmail(ctx, tx, actorID, consentingID, "follow"); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := enqueueFollowActivityEmail(ctx, tx, actorID, declinedID, "follow"); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := enqueueNewPostActivityEmails(ctx, tx, actorID, "public", "A post for people who follow me"); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	var followerMail, postMail, declinedMail int
	if err := db.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE preference_category='new_follower'),
		       count(*) FILTER (WHERE preference_category='new_post'),
		       count(*) FILTER (WHERE preference_user_id=$2::uuid)
		FROM testagram_mail.outbox
		WHERE preference_user_id=$1::uuid OR preference_user_id=$2::uuid
	`, consentingID, declinedID).Scan(&followerMail, &postMail, &declinedMail); err != nil {
		t.Fatal(err)
	}
	if followerMail != 1 || postMail != 1 || declinedMail != 0 {
		t.Fatalf("queued social email counts: follower=%d post=%d declined=%d; want 1,1,0", followerMail, postMail, declinedMail)
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
