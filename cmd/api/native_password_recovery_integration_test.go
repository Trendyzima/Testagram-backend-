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

	reset := func(token, password string) *httptest.ResponseRecorder {
		body, err := json.Marshal(map[string]string{"token": token, "password": password})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/auth/reset-password", strings.NewReader(string(body)))
		req.RemoteAddr = "192.0.2.11:12345"
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
	if got := reset("invalid-recovery-capability-00000000000000000000000000000000", "NewRecoveryPassword-456!"); got.Code != http.StatusTooManyRequests {
		t.Fatalf("fourth attempt against one token status=%d body=%s; want rate limit", got.Code, got.Body.String())
	}

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
