package main

import (
	"bytes"
	"context"
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

func TestNativeSignupCreatesUnverifiedIdentityAndQueuesVerificationEmail(t *testing.T) {
	db := nativeRecoveryTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	t.Setenv("TESTAGRAM_MAIL_ENCRYPTION_KEY", strings.Repeat("7b", 32))
	t.Setenv("AUTH_PUBLIC_BASE_URL", "https://www.testagram.site")
	t.Setenv("TESTAGRAM_AUTH_JWT_SECRET", strings.Repeat("s", 48))

	svc := &service{db: db}
	type signupFixture struct {
		userID   string
		email    string
		username string
		otp      string
		token    string
		outboxID int64
	}
	createAccount := func(label string) signupFixture {
		t.Helper()
		stamp := time.Now().UTC().UnixNano()
		email := fmt.Sprintf("native-%s-%d@example.test", label, stamp)
		username := fmt.Sprintf("n%s%d", label, stamp)
		var beforeID int64
		if err := db.QueryRow(ctx, "SELECT COALESCE(max(id),0) FROM testagram_mail.outbox").Scan(&beforeID); err != nil {
			t.Fatal(err)
		}

		body, err := json.Marshal(authInput{
			Email: email, Username: username, Password: "correct-horse-battery-staple-2026",
			TermsAccepted: true, PrivacyAccepted: true, AgeConfirmed: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/auth/signup", strings.NewReader(string(body)))
		req.RemoteAddr = fmt.Sprintf("192.0.2.%d:12345", 210+len(label))
		rec := httptest.NewRecorder()
		if !svc.authRespond(rec, req, "signup") {
			t.Fatal("public signup route was not handled")
		}
		if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), "Check your email") {
			t.Fatalf("ordinary signup should queue email verification without identity review: status=%d body=%s", rec.Code, rec.Body.String())
		}

		var fixture signupFixture
		fixture.email, fixture.username = email, username
		var verified bool
		var otpHash, tokenHash []byte
		if err := db.QueryRow(ctx, `
			SELECT user_id::text,email_verified_at IS NOT NULL,
			       verification_otp_hash,verification_token_hash
			FROM testagram_auth.identities WHERE email_normalized=$1
		`, email).Scan(&fixture.userID, &verified, &otpHash, &tokenHash); err != nil {
			t.Fatalf("ordinary signup did not persist its native identity: %v", err)
		}
		if fixture.userID == "" || verified || len(otpHash) != 32 || len(tokenHash) != 32 {
			t.Fatalf("signup identity state user=%q verified=%v otp_hash=%d token_hash=%d", fixture.userID, verified, len(otpHash), len(tokenHash))
		}
		var applications int
		if err := db.QueryRow(ctx, "SELECT count(*) FROM testagram_identity.identity_applications WHERE email=$1", email).Scan(&applications); err != nil {
			t.Fatal(err)
		}
		if applications != 0 {
			t.Fatalf("ordinary signup unexpectedly created %d identity-review applications", applications)
		}

		if err := db.QueryRow(ctx, `
			SELECT id,payload_ciphertext,payload_nonce
			FROM testagram_mail.outbox WHERE id>$1 ORDER BY id LIMIT 1
		`, beforeID).Scan(&fixture.outboxID, &otpHash, &tokenHash); err != nil {
			t.Fatalf("signup committed without a verification email in the transactional outbox: %v", err)
		}
		aead, err := mailCipher()
		if err != nil {
			t.Fatal(err)
		}
		plain, err := aead.Open(nil, tokenHash, otpHash, []byte("testagram-mail-v1"))
		if err != nil {
			t.Fatalf("verification email payload could not be decrypted: %v", err)
		}
		var queued queuedMail
		if err := json.Unmarshal(plain, &queued); err != nil {
			t.Fatal(err)
		}
		if queued.Recipient != email || queued.Subject != "Your Testagram verification code" {
			t.Fatalf("verification email does not match signup: recipient=%q subject=%q", queued.Recipient, queued.Subject)
		}
		const otpMarker = `letter-spacing:8px">`
		otpStart := strings.Index(queued.HTML, otpMarker)
		if otpStart < 0 || len(queued.HTML) < otpStart+len(otpMarker)+6 {
			t.Fatalf("verification email is missing its six-digit OTP: %s", queued.HTML)
		}
		fixture.otp = queued.HTML[otpStart+len(otpMarker) : otpStart+len(otpMarker)+6]
		for _, digit := range fixture.otp {
			if digit < '0' || digit > '9' {
				t.Fatalf("verification email contains malformed OTP %q", fixture.otp)
			}
		}
		const linkMarker = "/verify-email?token="
		linkStart := strings.Index(queued.HTML, linkMarker)
		if linkStart < 0 {
			t.Fatalf("verification email is missing its one-time link: %s", queued.HTML)
		}
		linkStart += len(linkMarker)
		linkEnd := strings.Index(queued.HTML[linkStart:], "\"")
		if linkEnd < 0 {
			t.Fatalf("verification email link is malformed: %s", queued.HTML)
		}
		fixture.token = queued.HTML[linkStart : linkStart+linkEnd]
		if len(fixture.token) < 32 {
			t.Fatalf("verification link token is too short: %q", fixture.token)
		}

		// An unverified account must not be able to obtain a session.
		loginBody, _ := json.Marshal(map[string]string{
			"email": email, "password": "correct-horse-battery-staple-2026",
		})
		loginReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(string(loginBody)))
		loginReq.RemoteAddr = fmt.Sprintf("192.0.2.%d:12346", 210+len(label))
		loginRec := httptest.NewRecorder()
		svc.authRespond(loginRec, loginReq, "login")
		if loginRec.Code != http.StatusForbidden || !strings.Contains(loginRec.Body.String(), "EMAIL_VERIFICATION_REQUIRED") {
			t.Fatalf("unverified account login status=%d body=%s; expected EMAIL_VERIFICATION_REQUIRED", loginRec.Code, loginRec.Body.String())
		}
		t.Cleanup(func() {
			_, _ = db.Exec(context.Background(), "DELETE FROM testagram_auth.sessions WHERE user_id=$1::uuid", fixture.userID)
			_, _ = db.Exec(context.Background(), "DELETE FROM testagram_local.accounts WHERE user_id=$1::uuid", fixture.userID)
			_, _ = db.Exec(context.Background(), "DELETE FROM testagram_auth.registration_consents WHERE user_id=$1::uuid", fixture.userID)
			_, _ = db.Exec(context.Background(), "DELETE FROM testagram_auth.identities WHERE user_id=$1::uuid", fixture.userID)
			_, _ = db.Exec(context.Background(), "DELETE FROM testagram_mail.outbox WHERE id=$1", fixture.outboxID)
		})
		return fixture
	}

	verify := func(payload map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/auth/verify-email", strings.NewReader(string(body)))
		req.RemoteAddr = "192.0.2.220:12345"
		rec := httptest.NewRecorder()
		if !svc.authRespond(rec, req, "verify-email") {
			t.Fatal("verify-email route was not handled")
		}
		return rec
	}

	// The real one-time link path must activate a basic account without creating
	// an identity-review application, and a consumed link must not be reusable.
	linkAccount := createAccount("link")

	// A username collision is actionable and must not masquerade as a queued
	// verification email for a different email address.
	duplicateEmail := fmt.Sprintf("duplicate-username-%d@example.test", time.Now().UTC().UnixNano())
	var beforeDuplicateOutbox int64
	if err := db.QueryRow(ctx, "SELECT COALESCE(max(id),0) FROM testagram_mail.outbox").Scan(&beforeDuplicateOutbox); err != nil {
		t.Fatal(err)
	}
	duplicateBody, err := json.Marshal(authInput{
		Email: duplicateEmail, Username: linkAccount.username, Password: "correct-horse-battery-staple-2026",
			TermsAccepted: true, PrivacyAccepted: true, AgeConfirmed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	duplicateReq := httptest.NewRequest(http.MethodPost, "/api/auth/signup", strings.NewReader(string(duplicateBody)))
	duplicateReq.RemoteAddr = "192.0.2.215:12345"
	duplicateRec := httptest.NewRecorder()
	svc.authRespond(duplicateRec, duplicateReq, "signup")
	if duplicateRec.Code != http.StatusConflict || !strings.Contains(duplicateRec.Body.String(), "USERNAME_TAKEN") {
		t.Fatalf("duplicate username status=%d body=%s; expected actionable USERNAME_TAKEN", duplicateRec.Code, duplicateRec.Body.String())
	}
	var duplicateIdentityCount, duplicateMailCount int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM testagram_auth.identities WHERE email_normalized=$1", duplicateEmail).Scan(&duplicateIdentityCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "SELECT count(*) FROM testagram_mail.outbox WHERE id>$1", beforeDuplicateOutbox).Scan(&duplicateMailCount); err != nil {
		t.Fatal(err)
	}
	if duplicateIdentityCount != 0 || duplicateMailCount != 0 {
		t.Fatalf("username conflict left an orphan identity/email: identities=%d queued_mail=%d", duplicateIdentityCount, duplicateMailCount)
	}
	linkVerified := verify(map[string]string{"token": linkAccount.token})
	if linkVerified.Code != http.StatusOK || !strings.Contains(linkVerified.Body.String(), "Email verified") {
		t.Fatalf("one-time email link verification status=%d body=%s", linkVerified.Code, linkVerified.Body.String())
	}
	linkReplay := verify(map[string]string{"token": linkAccount.token})
	if linkReplay.Code != http.StatusBadRequest || !strings.Contains(linkReplay.Body.String(), "INVALID_TOKEN") {
		t.Fatalf("consumed email link replay status=%d body=%s", linkReplay.Code, linkReplay.Body.String())
	}

	// The six-digit OTP path must provide the same activation guarantees.
	otpAccount := createAccount("otp")
	otpVerified := verify(map[string]string{"email": otpAccount.email, "token": otpAccount.otp})
	if otpVerified.Code != http.StatusOK || !strings.Contains(otpVerified.Body.String(), "Email verified") {
		t.Fatalf("OTP email verification status=%d body=%s", otpVerified.Code, otpVerified.Body.String())
	}
	otpReplay := verify(map[string]string{"email": otpAccount.email, "token": otpAccount.otp})
	if otpReplay.Code != http.StatusBadRequest || !strings.Contains(otpReplay.Body.String(), "INVALID_OTP") {
		t.Fatalf("consumed OTP replay status=%d body=%s", otpReplay.Code, otpReplay.Body.String())
	}

	for _, fixture := range []signupFixture{linkAccount, otpAccount} {
		var verified bool
		if err := db.QueryRow(ctx, "SELECT email_verified_at IS NOT NULL FROM testagram_auth.identities WHERE user_id=$1::uuid", fixture.userID).Scan(&verified); err != nil || !verified {
			t.Fatalf("verified identity state user=%s verified=%v err=%v", fixture.userID, verified, err)
		}
		var profiles int
		if err := db.QueryRow(ctx, "SELECT count(*) FROM testagram_local.accounts WHERE user_id=$1::uuid", fixture.userID).Scan(&profiles); err != nil || profiles != 1 {
			t.Fatalf("email verification profile count user=%s count=%d err=%v", fixture.userID, profiles, err)
		}
		loginPayload, err := json.Marshal(map[string]string{
			"email": fixture.email, "password": "correct-horse-battery-staple-2026",
		})
		if err != nil {
			t.Fatal(err)
		}
		loginReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(string(loginPayload)))
		loginReq.RemoteAddr = "192.0.2.230:12345"
		loginRec := httptest.NewRecorder()
		svc.authRespond(loginRec, loginReq, "login")
		if loginRec.Code != http.StatusOK {
			t.Fatalf("verified basic account could not sign in: status=%d body=%s", loginRec.Code, loginRec.Body.String())
		}
	}
}

func TestNativeSignupRejectsUsernameReservedByExistingLocalProfile(t *testing.T) {
	db := nativeRecoveryTestDatabase(t)
	ctx := context.Background()
	username := fmt.Sprintf("legacy%d", time.Now().UTC().UnixNano())
	var userID string
	if err := db.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO testagram_local.accounts(user_id,username,display_name)
		VALUES($1::uuid,$2,'Existing local profile')
	`, userID, username); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = db.Exec(context.Background(), "DELETE FROM testagram_local.accounts WHERE user_id=$1::uuid", userID)
	}()

	email := fmt.Sprintf("legacy-username-%d@example.test", time.Now().UTC().UnixNano())
	body, err := json.Marshal(authInput{
		Email: email, Username: username, Password: "correct-horse-battery-staple-2026",
			TermsAccepted: true, PrivacyAccepted: true, AgeConfirmed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/signup", strings.NewReader(string(body)))
	req.RemoteAddr = "192.0.2.216:12345"
	rec := httptest.NewRecorder()
	if !(&service{db: db}).authRespond(rec, req, "signup") {
		t.Fatal("public signup route was not handled")
	}
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "USERNAME_TAKEN") {
		t.Fatalf("existing local-profile username status=%d body=%s; expected USERNAME_TAKEN", rec.Code, rec.Body.String())
	}
	var identityCount int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM testagram_auth.identities WHERE email_normalized=$1", email).Scan(&identityCount); err != nil {
		t.Fatal(err)
	}
	if identityCount != 0 {
		t.Fatalf("signup created %d credentials for a username already reserved by a local profile", identityCount)
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
