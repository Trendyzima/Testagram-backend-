package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func nativeRecoveryTestDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for native password recovery integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(ctx); err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}

func nativeRecoveryTestIdentity(t *testing.T, db *pgxpool.Pool) (string, string, int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	suffix := fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	email := "recovery-" + suffix + "@example.test"
	username := "r" + suffix[len(suffix)-17:]
	passwordHash, err := authPasswordHash("OldRecoveryPassword-123!")
	if err != nil {
		t.Fatal(err)
	}
	var userID string
	if err := db.QueryRow(ctx, `
		INSERT INTO testagram_auth.identities(email,username,password_hash,email_verified_at)
		VALUES($1,$2,$3,now())
		RETURNING user_id::text
	`, email, username, passwordHash).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := db.QueryRow(ctx, `
		SELECT token_version FROM testagram_auth.identities WHERE user_id=$1::uuid
	`, userID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return userID, email, version
}

func TestNativePasswordRecoveryRequestIsAntiEnumeration(t *testing.T) {
	db := nativeRecoveryTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	userID, email, _ := nativeRecoveryTestIdentity(t, db)

	// Deliberately make delivery unavailable. The public response must remain
	// the same for a known and an unknown address, while the failure is logged
	// without exposing the recovery token or recipient address.
	for _, key := range []string{"SMTP_HOST", "SMTP_USERNAME", "SMTP_PASSWORD", "AUTH_FROM_EMAIL"} {
		t.Setenv(key, "")
	}
	t.Setenv("AUTH_PUBLIC_BASE_URL", "https://www.testagram.site")
	svc := &service{db: db}

	request := func(address string) *httptest.ResponseRecorder {
		body, err := json.Marshal(map[string]string{"email": address})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/auth/forgot-password", strings.NewReader(string(body)))
		req.RemoteAddr = "192.0.2.10:12345"
		rec := httptest.NewRecorder()
		if !svc.authRespond(rec, req, "forgot-password") {
			t.Fatal("forgot-password handler did not handle request")
		}
		return rec
	}

	known := request(email)
	unknown := request("not-registered-" + fmt.Sprintf("%d", time.Now().UnixNano()) + "@example.test")
	if known.Code != http.StatusAccepted || unknown.Code != http.StatusAccepted {
		t.Fatalf("anti-enumeration status known=%d unknown=%d", known.Code, unknown.Code)
	}
	if known.Body.String() != unknown.Body.String() {
		t.Fatalf("forgot-password responses disclose account existence: known=%q unknown=%q", known.Body.String(), unknown.Body.String())
	}
	var resetRows int
	if err := db.QueryRow(ctx, `
		SELECT count(*) FROM testagram_auth.password_reset_tokens
		WHERE user_id=$1::uuid AND consumed_at IS NULL AND expires_at>now()
	`, userID).Scan(&resetRows); err != nil {
		t.Fatal(err)
	}
	if resetRows != 1 {
		t.Fatalf("known verified account should have one pending recovery token; got %d", resetRows)
	}
	var ciphertext, nonce []byte
	if err := db.QueryRow(ctx, `
		SELECT payload_ciphertext,payload_nonce
		FROM testagram_mail.outbox
		WHERE status='pending'
		ORDER BY id DESC LIMIT 1
	`).Scan(&ciphertext, &nonce); err != nil {
		t.Fatalf("recovery token committed without a queued email: %v", err)
	}
	aead, err := mailCipher()
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, []byte("testagram-mail-v1"))
	if err != nil {
		t.Fatalf("recovery email payload could not be decrypted: %v", err)
	}
	var queued queuedMail
	if err := json.Unmarshal(plaintext, &queued); err != nil {
		t.Fatal(err)
	}
	if queued.Recipient != email || queued.Subject != "Reset your Testagram password" ||
		!strings.Contains(queued.HTML, "/reset-password?token=") {
		t.Fatalf("recovery email outbox payload does not match the account: %#v", queued)
	}
}

func TestNativePasswordResetConsumesTokenRevokesSessionsAndAllowsNewLogin(t *testing.T) {
	db := nativeRecoveryTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	userID, email, version := nativeRecoveryTestIdentity(t, db)
	t.Setenv("TESTAGRAM_AUTH_JWT_SECRET", strings.Repeat("s", 48))

	svc := &service{db: db}
	invalidToken, err := authRandomToken()
	if err != nil {
		t.Fatal(err)
	}
	oldSession, err := issueNativeSession(ctx, db, userID, email, version, "recovery-integration", "192.0.2.11")
	if err != nil {
		t.Fatal(err)
	}
	resetToken, err := authRandomToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO testagram_auth.password_reset_tokens(token_hash,user_id,expires_at,request_ip_hash)
		VALUES($1,$2::uuid,now()+interval '30 minutes',$3)
	`, authTokenHash(resetToken), userID, authFingerprint("192.0.2.11")); err != nil {
		t.Fatal(err)
	}

	resetIP := "192.0.2.11"
	reset := func(token, password string) *httptest.ResponseRecorder {
		body, err := json.Marshal(map[string]string{"token": token, "password": password})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/auth/reset-password", strings.NewReader(string(body)))
		req.RemoteAddr = resetIP + ":12345"
		rec := httptest.NewRecorder()
		if !svc.authRespond(rec, req, "reset-password") {
			t.Fatal("reset-password handler did not handle request")
		}
		return rec
	}

	// Invalid links are rejected before Argon2id runs. Repeated attempts against
	// one capability are bounded independently of the caller's IP bucket.
	for attempt := 0; attempt < 3; attempt++ {
		if got := reset(invalidToken, "NewRecoveryPassword-456!"); got.Code != http.StatusBadRequest {
			t.Fatalf("invalid reset attempt %d status=%d body=%s", attempt+1, got.Code, got.Body.String())
		}
	}
	if got := reset(invalidToken, "NewRecoveryPassword-456!"); got.Code != http.StatusTooManyRequests {
		t.Fatalf("fourth attempt against one token status=%d body=%s; want rate limit", got.Code, got.Body.String())
	}

	// Keep the valid capability check independent from the intentionally exhausted
	// invalid-token/IP buckets above; the account session still uses the original IP.
	resetIP = "192.0.2.13"
	response := reset(resetToken, "NewRecoveryPassword-456!")
	if response.Code != http.StatusOK {
		t.Fatalf("valid reset status=%d body=%s", response.Code, response.Body.String())
	}

	var storedHash string
	var nextVersion int64
	var consumedAt *time.Time
	if err := db.QueryRow(ctx, `
		SELECT i.password_hash,i.token_version,t.consumed_at
		FROM testagram_auth.identities i
		JOIN testagram_auth.password_reset_tokens t ON t.user_id=i.user_id
		WHERE i.user_id=$1::uuid AND t.token_hash=$2
	`, userID, authTokenHash(resetToken)).Scan(&storedHash, &nextVersion, &consumedAt); err != nil {
		t.Fatal(err)
	}
	if !authPasswordVerify(storedHash, "NewRecoveryPassword-456!") ||
		authPasswordVerify(storedHash, "OldRecoveryPassword-123!") ||
		nextVersion != version+1 || consumedAt == nil {
		t.Fatalf("reset invariants failed: version=%d previous=%d consumed=%v", nextVersion, version, consumedAt)
	}

	if _, status, err := verifyNativeAccessToken(ctx, db, oldSession.AccessToken); err == nil || status != http.StatusUnauthorized {
		t.Fatalf("password reset left old access token usable: status=%d err=%v", status, err)
	}
	if _, err := svc.rotateNativeRefresh(ctx, oldSession.RefreshToken, "recovery-integration", "192.0.2.11"); err == nil {
		t.Fatal("password reset left old refresh token usable")
	}
	if replay := reset(resetToken, "AnotherRecoveryPassword-789!"); replay.Code != http.StatusBadRequest {
		t.Fatalf("single-use reset token replay status=%d body=%s", replay.Code, replay.Body.String())
	}

	loginBody, _ := json.Marshal(map[string]string{"email": email, "password": "NewRecoveryPassword-456!"})
	loginReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(string(loginBody)))
	loginReq.RemoteAddr = "192.0.2.12:12345"
	loginResp := httptest.NewRecorder()
	if !svc.authRespond(loginResp, loginReq, "login") ||
		loginResp.Code != http.StatusOK ||
		!strings.Contains(loginResp.Body.String(), "access_token") {
		t.Fatalf("login with reset password failed: status=%d body=%s", loginResp.Code, loginResp.Body.String())
	}

	oldLoginBody, _ := json.Marshal(map[string]string{"email": email, "password": "OldRecoveryPassword-123!"})
	oldLoginReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(string(oldLoginBody)))
	oldLoginReq.RemoteAddr = "192.0.2.13:12345"
	oldLoginResp := httptest.NewRecorder()
	svc.authRespond(oldLoginResp, oldLoginReq, "login")
	if oldLoginResp.Code != http.StatusUnauthorized {
		t.Fatalf("old password remained valid after reset: status=%d body=%s", oldLoginResp.Code, oldLoginResp.Body.String())
	}
}
// TestNativePasswordResetRejectsExpiredToken proves an expired capability
// cannot change credentials even when its hash is otherwise valid.
func TestNativePasswordResetRejectsExpiredToken(t *testing.T) {
	db := nativeRecoveryTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	userID, _, _ := nativeRecoveryTestIdentity(t, db)
	defer func() {
		_, _ = db.Exec(context.Background(), "DELETE FROM testagram_auth.password_reset_tokens WHERE user_id=$1::uuid", userID)
		_, _ = db.Exec(context.Background(), "DELETE FROM testagram_auth.identities WHERE user_id=$1::uuid", userID)
	}()
	token, err := authRandomToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO testagram_auth.password_reset_tokens(token_hash,user_id,expires_at,request_ip_hash)
		VALUES($1,$2::uuid,now()-interval '1 second',$3)
	`, authTokenHash(token), userID, authFingerprint("192.0.2.31")); err != nil {
		t.Fatal(err)
	}
	var beforeHash string
	if err := db.QueryRow(ctx, "SELECT password_hash FROM testagram_auth.identities WHERE user_id=$1::uuid", userID).Scan(&beforeHash); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]string{"token": token, "password": "NewExpiredPassword-456!"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/reset-password", strings.NewReader(string(body)))
	req.RemoteAddr = "192.0.2.31:12345"
	rec := httptest.NewRecorder()
	if !(&service{db: db}).authRespond(rec, req, "reset-password") {
		t.Fatal("reset-password handler did not handle expired token")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expired reset token status=%d body=%s; want 400", rec.Code, rec.Body.String())
	}
	var afterHash string
	if err := db.QueryRow(ctx, "SELECT password_hash FROM testagram_auth.identities WHERE user_id=$1::uuid", userID).Scan(&afterHash); err != nil {
		t.Fatal(err)
	}
	if afterHash != beforeHash {
		t.Fatal("expired reset token changed the account password")
	}
}

// TestNativeVerificationResendRotatesCredentialsAndQueuesEncryptedMail
// verifies resend invalidates the previous link/code and atomically queues
// the replacement message before the new link can initialize the profile.
func TestNativeVerificationResendRotatesCredentialsAndQueuesEncryptedMail(t *testing.T) {
	db := nativeRecoveryTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	t.Setenv("TESTAGRAM_MAIL_ENCRYPTION_KEY", strings.Repeat("6d", 32))
	email := fmt.Sprintf("resend-%d@example.test", time.Now().UTC().UnixNano())
	username := fmt.Sprintf("rs%d", time.Now().UTC().UnixNano()%1_000_000_000_000)
	oldToken, err := authRandomToken()
	if err != nil {
		t.Fatal(err)
	}
	oldOTP, err := authRandomOTP()
	if err != nil {
		t.Fatal(err)
	}
	oldOTPHash, err := authOTPHash(oldOTP)
	if err != nil {
		t.Fatal(err)
	}
	passwordHash, err := authPasswordHash("OldResendPassword-123!")
	if err != nil {
		t.Fatal(err)
	}
	var userID string
	if err := db.QueryRow(ctx, `
		INSERT INTO testagram_auth.identities(
			email,username,password_hash,verification_token_hash,verification_expires_at,
			verification_otp_hash,verification_otp_expires_at,verification_otp_attempts
		) VALUES($1,$2,$3,$4,now()+interval '24 hours',$5,now()+interval '10 minutes',0)
		RETURNING user_id::text
	`, email, username, passwordHash, authTokenHash(oldToken), oldOTPHash).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = db.Exec(context.Background(), "DELETE FROM testagram_mail.outbox WHERE recipient_fingerprint=$1", mustMailFingerprint(t, email))
		_, _ = db.Exec(context.Background(), "DELETE FROM testagram_local.accounts WHERE user_id=$1::uuid", userID)
		_, _ = db.Exec(context.Background(), "DELETE FROM testagram_auth.identities WHERE user_id=$1::uuid", userID)
	}()
	body, err := json.Marshal(map[string]string{"email": email})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/resend-verification", strings.NewReader(string(body)))
	req.RemoteAddr = "192.0.2.41:12345"
	rec := httptest.NewRecorder()
	if !(&service{db: db}).authRespond(rec, req, "resend-verification") {
		t.Fatal("resend-verification handler did not handle request")
	}
	if rec.Code != http.StatusAccepted {
		t.Fatalf("resend status=%d body=%s; want 202", rec.Code, rec.Body.String())
	}
	var newTokenHash, newOTPHash []byte
	var linkExpiry, otpExpiry time.Time
	if err := db.QueryRow(ctx, `
		SELECT verification_token_hash,verification_otp_hash,verification_expires_at,verification_otp_expires_at
		FROM testagram_auth.identities WHERE user_id=$1::uuid
	`, userID).Scan(&newTokenHash, &newOTPHash, &linkExpiry, &otpExpiry); err != nil {
		t.Fatal(err)
	}
	if len(newTokenHash) != 32 || len(newOTPHash) != 32 ||
		string(newTokenHash) == string(authTokenHash(oldToken)) ||
		string(newOTPHash) == string(oldOTPHash) ||
		!linkExpiry.After(time.Now()) || !otpExpiry.After(time.Now()) {
		t.Fatal("resend did not rotate both credentials with fresh expiry windows")
	}
	var ciphertext, nonce []byte
	if err := db.QueryRow(ctx, `
		SELECT payload_ciphertext,payload_nonce FROM testagram_mail.outbox
		WHERE recipient_fingerprint=$1 AND status='pending' ORDER BY id DESC LIMIT 1
	`, mustMailFingerprint(t, email)).Scan(&ciphertext, &nonce); err != nil {
		t.Fatalf("resend did not queue a replacement verification email: %v", err)
	}
	if strings.Contains(string(ciphertext), email) || strings.Contains(string(ciphertext), oldToken) {
		t.Fatal("verification message content was persisted in plaintext")
	}
	aead, err := mailCipher()
	if err != nil {
		t.Fatal(err)
	}
	plain, err := aead.Open(nil, nonce, ciphertext, []byte("testagram-mail-v1"))
	if err != nil {
		t.Fatalf("replacement verification message could not be decrypted: %v", err)
	}
	var queued queuedMail
	if err := json.Unmarshal(plain, &queued); err != nil {
		t.Fatal(err)
	}
	if queued.Recipient != email || queued.Subject != "Your Testagram verification code" {
		t.Fatalf("replacement email does not match recipient: %#v", queued)
	}
	const otpMarker = `letter-spacing:8px">`
	otpStart := strings.Index(queued.HTML, otpMarker)
	if otpStart < 0 || len(queued.HTML) < otpStart+len(otpMarker)+6 {
		t.Fatal("replacement verification email does not contain a six-digit code")
	}
	newOTP := queued.HTML[otpStart+len(otpMarker) : otpStart+len(otpMarker)+6]
	emailOTPHash, err := authOTPHash(newOTP)
	if err != nil {
		t.Fatal(err)
	}
	const linkMarker = "/verify-email?token="
	linkStart := strings.Index(queued.HTML, linkMarker)
	if linkStart < 0 {
		t.Fatal("replacement verification email does not contain a verification link")
	}
	linkStart += len(linkMarker)
	linkEnd := strings.Index(queued.HTML[linkStart:], "\"")
	if linkEnd < 0 {
		t.Fatal("replacement verification link is malformed")
	}
	newToken := queued.HTML[linkStart : linkStart+linkEnd]
	if string(newTokenHash) != string(authTokenHash(newToken)) || string(newOTPHash) != string(emailOTPHash) {
		t.Fatal("replacement email credentials do not match the committed identity hashes")
	}
	verify := func(token string) *httptest.ResponseRecorder {
		payload, err := json.Marshal(map[string]string{"token": token})
		if err != nil {
			t.Fatal(err)
		}
		verifyReq := httptest.NewRequest(http.MethodPost, "/api/auth/verify-email", strings.NewReader(string(payload)))
		verifyReq.RemoteAddr = "192.0.2.42:12345"
		verifyRec := httptest.NewRecorder()
		(&service{db: db}).authRespond(verifyRec, verifyReq, "verify-email")
		return verifyRec
	}
	if got := verify(oldToken); got.Code != http.StatusBadRequest {
		t.Fatalf("superseded verification token remained valid: status=%d body=%s", got.Code, got.Body.String())
	}
	if got := verify(newToken); got.Code != http.StatusOK {
		t.Fatalf("replacement verification link failed: status=%d body=%s", got.Code, got.Body.String())
	}
	if got := verify(newToken); got.Code != http.StatusBadRequest {
		t.Fatalf("replacement verification link replay status=%d; want 400", got.Code)
	}
	var verified bool
	var profiles int
	if err := db.QueryRow(ctx, "SELECT email_verified_at IS NOT NULL FROM testagram_auth.identities WHERE user_id=$1::uuid", userID).Scan(&verified); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "SELECT count(*) FROM testagram_local.accounts WHERE user_id=$1::uuid", userID).Scan(&profiles); err != nil {
		t.Fatal(err)
	}
	if !verified || profiles != 1 {
		t.Fatalf("resend verification final state verified=%v profiles=%d; want true,1", verified, profiles)
	}
}
