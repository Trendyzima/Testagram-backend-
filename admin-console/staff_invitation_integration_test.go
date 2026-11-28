package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNativeStaffInvitationLifecycleIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect to isolated PostgreSQL: %v", err)
	}
	defer pool.Close()

	mailKey := bytes.Repeat([]byte{0x6a}, 32)
	t.Setenv("TESTAGRAM_MAIL_ENCRYPTION_KEY", hex.EncodeToString(mailKey))
	t.Setenv("ADMIN_PUBLIC_URL", "https://ops.example.test")
	t.Setenv("TESTAGRAM_AUTH_JWT_SECRET", strings.Repeat("native-staff-invitation-integration-secret-", 2))
	const rpid = "ops.example.test"

	var requesterID, approverOneID, approverTwoID, targetID, sessionID string
	if err := pool.QueryRow(ctx, "SELECT gen_random_uuid()::text,gen_random_uuid()::text,gen_random_uuid()::text,gen_random_uuid()::text,gen_random_uuid()::text").
		Scan(&requesterID, &approverOneID, &approverTwoID, &targetID, &sessionID); err != nil {
		t.Fatalf("generate fixture IDs: %v", err)
	}
	targetEmail := "staff-invite-" + targetID[:8] + "@example.test"
	owners := []string{requesterID, approverOneID, approverTwoID}
	for index, ownerID := range owners {
		email := fmt.Sprintf("staff-invite-owner-%d-%s@example.test", index, ownerID[:8])
		username := fmt.Sprintf("staffinviteowner%d_%s", index, ownerID[:8])
		if _, err := pool.Exec(ctx, `
			INSERT INTO testagram_auth.identities(user_id,email,username,password_hash,email_verified_at)
			VALUES($1::uuid,$2,$3,'test-only-invalid-password-hash',now())
		`, ownerID, email, username); err != nil {
			t.Fatalf("insert owner identity: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO testagram_auth.staff_principals(user_id,enabled,mfa_enrolled_at,enabled_at,enabled_by)
			VALUES($1::uuid,TRUE,now(),now(),$1::uuid)
		`, ownerID); err != nil {
			t.Fatalf("insert owner principal: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO testagram_auth.staff_role_grants(user_id,role,granted_by)
			VALUES($1::uuid,'owner',$1::uuid)
		`, ownerID); err != nil {
			t.Fatalf("grant owner role: %v", err)
		}
		credentialID := []byte("native-invitation-webauthn-credential-" + ownerID)
		if _, err := pool.Exec(ctx, `
			INSERT INTO testagram_auth.staff_webauthn_credentials
			  (rpid,user_id,credential_id,credential,user_verified,clone_warning)
			VALUES($1,$2::uuid,$3,'test-only-encrypted-credential',TRUE,FALSE)
		`, rpid, ownerID, credentialID); err != nil {
			t.Fatalf("insert owner WebAuthn credential: %v", err)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO testagram_auth.identities(user_id,email,username,password_hash,email_verified_at)
		VALUES($1::uuid,$2,$3,'test-only-invalid-password-hash',now())
	`, targetID, targetEmail, "staffinvite_"+targetID[:8]); err != nil {
		t.Fatalf("insert invitation target identity: %v", err)
	}

	payload, err := json.Marshal(staffInvitationPayload{Email: targetEmail, Role: "engineer"})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	var requestID string
	err = pool.QueryRow(ctx, `
		INSERT INTO testagram_auth.native_sensitive_action_requests
		  (action_type,requested_by_user_id,requested_by_service,target_reference,reason,payload,payload_sha256,required_approvals)
		VALUES('staff.invite',$1::uuid,'staff',$2,'Integration test invitation with verified mailbox',$3::jsonb,$4,2)
		RETURNING id::text
	`, requesterID, targetID, string(payload), hex.EncodeToString(digest[:])).Scan(&requestID)
	if err != nil {
		t.Fatalf("create invitation approval request: %v", err)
	}

	admin := testApp(t)
	admin.database = pool
	for index, approverID := range []string{approverOneID, approverTwoID} {
		credentialID := []byte("native-invitation-webauthn-credential-" + approverID)
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin approval transaction %d: %v", index+1, err)
		}
		state, _, count, decisionErr := recordSensitiveActionDecision(
			ctx, tx, requestID, "staff.invite", requesterID, approverID,
			rpid, credentialID, "approved", "Independent owner approval for invitation",
		)
		if decisionErr != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("record owner approval %d: %v", index+1, decisionErr)
		}
		if index == 0 && (state != "pending" || count != 1) {
			_ = tx.Rollback(ctx)
			t.Fatalf("first approval state=%s count=%d; want pending/1", state, count)
		}
		if index == 1 && (state != "approved" || count != 2) {
			_ = tx.Rollback(ctx)
			t.Fatalf("second approval state=%s count=%d; want approved/2", state, count)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit owner approval %d: %v", index+1, err)
		}
	}

	var invitationID string
	var storedHash, ciphertext, nonce []byte
	fingerprintMAC := hmac.New(sha256.New, mailKey)
	_, _ = fingerprintMAC.Write([]byte(strings.ToLower(targetEmail)))
	fingerprint := fingerprintMAC.Sum(nil)
	if err := pool.QueryRow(ctx, `
		SELECT ni.id::text,ni.token_sha256,o.payload_ciphertext,o.payload_nonce
		FROM testagram_auth.native_staff_invitations ni
		JOIN testagram_mail.outbox o ON o.recipient_fingerprint=$2 AND o.status='pending'
		WHERE ni.request_id=$1::uuid
		ORDER BY o.id DESC LIMIT 1
	`, requestID, fingerprint).Scan(&invitationID, &storedHash, &ciphertext, &nonce); err != nil {
		t.Fatalf("approved invitation or encrypted mail outbox row missing: %v", err)
	}
	block, err := aes.NewCipher(mailKey)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := aead.Open(nil, nonce, ciphertext, []byte("testagram-mail-v1"))
	if err != nil {
		t.Fatalf("decrypt queued invitation email: %v", err)
	}
	var message staffInvitationMail
	if err := json.Unmarshal(plain, &message); err != nil {
		t.Fatalf("decode queued invitation email: %v", err)
	}
	if message.Recipient != targetEmail || !strings.Contains(message.Subject, "Testagram staff invitation") {
		t.Fatalf("unexpected invitation mail envelope: %#v", message)
	}
	linkStart := strings.Index(message.HTML, `href="`)
	if linkStart < 0 {
		t.Fatal("queued invitation email does not contain an acceptance link")
	}
	linkStart += len(`href="`)
	linkEnd := strings.Index(message.HTML[linkStart:], `"`)
	if linkEnd < 0 {
		t.Fatal("queued invitation acceptance link is malformed")
	}
	acceptURL, err := url.Parse(message.HTML[linkStart : linkStart+linkEnd])
	if err != nil {
		t.Fatalf("parse invitation acceptance URL: %v", err)
	}
	token := acceptURL.Query().Get("token")
	rawToken, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(rawToken) != 32 {
		t.Fatal("queued invitation link does not contain a 256-bit token")
	}
	tokenHash := sha256.Sum256(rawToken)
	if !hmac.Equal(storedHash, tokenHash[:]) {
		t.Fatal("persisted invitation digest does not match the emailed token")
	}
	if bytes.Contains(ciphertext, []byte(token)) {
		t.Fatal("plaintext invitation token was persisted in the mail outbox ciphertext field")
	}
	var lifecycleEvents int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM testagram_auth.native_staff_lifecycle_events
		WHERE request_id=$1::uuid AND action='invitation_created'
	`, requestID).Scan(&lifecycleEvents); err != nil || lifecycleEvents != 1 {
		t.Fatalf("invitation creation audit events=%d err=%v; want exactly one", lifecycleEvents, err)
	}

	var tokenVersion int64
	if err := pool.QueryRow(ctx, "SELECT token_version FROM testagram_auth.identities WHERE user_id=$1::uuid", targetID).Scan(&tokenVersion); err != nil {
		t.Fatalf("read invitee token version: %v", err)
	}
	refreshBytes := make([]byte, 32)
	if _, err := rand.Read(refreshBytes); err != nil {
		t.Fatal(err)
	}
	refreshToken := base64.RawURLEncoding.EncodeToString(refreshBytes)
	refreshHash := sha256.Sum256([]byte(refreshToken))
	if _, err := pool.Exec(ctx, `
		INSERT INTO testagram_auth.sessions(session_id,user_id,refresh_token_hash,family_id,token_version,expires_at)
		VALUES($1::uuid,$2::uuid,$3,gen_random_uuid(),$4,now()+interval '1 day')
	`, sessionID, targetID, refreshHash[:], tokenVersion); err != nil {
		t.Fatalf("create invitee native session: %v", err)
	}
	now := time.Now().UTC().Unix()
	claims := nativeJWTClaims{
		Subject: targetID, Email: targetEmail, Role: "authenticated",
		Issuer: "testagram-auth", Audience: "testagram-api",
		IssuedAt: now, ExpiresAt: now + 600, TokenVersion: tokenVersion, SessionID: sessionID,
	}
	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	claimsJSON, _ := json.Marshal(claims)
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claimsJSON)
	mac := hmac.New(sha256.New, []byte(os.Getenv("TESTAGRAM_AUTH_JWT_SECRET")))
	_, _ = mac.Write([]byte(unsigned))
	accessToken := unsigned + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	sessionResponse := httptest.NewRecorder()
	if err := admin.setSession(sessionResponse, session{
		AccessToken: accessToken, RefreshToken: refreshToken,
		AccessExpiresAt: now + 600, RefreshExpiresAt: now + 86400,
	}); err != nil {
		t.Fatalf("create encrypted invitee session cookie: %v", err)
	}
	cookie := sessionResponse.Result().Cookies()[0]
	body, _ := json.Marshal(map[string]string{"token": token})
	acceptRequest := httptest.NewRequest(http.MethodPost, "https://ops.example.test/api/staff/invitations/accept", bytes.NewReader(body))
	acceptRequest.AddCookie(cookie)
	acceptResponse := httptest.NewRecorder()
	admin.handleAcceptStaffInvitation(acceptResponse, acceptRequest)
	if acceptResponse.Code != http.StatusOK {
		t.Fatalf("accept invitation status=%d body=%s", acceptResponse.Code, acceptResponse.Body.String())
	}
	var grantedRole string
	if err := pool.QueryRow(ctx, `
		SELECT rg.role FROM testagram_auth.staff_role_grants rg
		JOIN testagram_auth.staff_principals sp ON sp.user_id=rg.user_id AND sp.enabled=TRUE
		WHERE rg.user_id=$1::uuid AND rg.revoked_at IS NULL
	`, targetID).Scan(&grantedRole); err != nil || grantedRole != "engineer" {
		t.Fatalf("accepted staff role=%q err=%v; want engineer", grantedRole, err)
	}
	var acceptedEvents int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM testagram_auth.native_staff_lifecycle_events
		WHERE request_id=$1::uuid AND action='invitation_accepted'
	`, requestID).Scan(&acceptedEvents); err != nil || acceptedEvents != 1 {
		t.Fatalf("invitation acceptance audit events=%d err=%v; want exactly one", acceptedEvents, err)
	}

	replay := httptest.NewRecorder()
	replayRequest := httptest.NewRequest(http.MethodPost, "https://ops.example.test/api/staff/invitations/accept", bytes.NewReader(body))
	replayRequest.AddCookie(cookie)
	admin.handleAcceptStaffInvitation(replay, replayRequest)
	if replay.Code != http.StatusConflict {
		t.Fatalf("replayed invitation status=%d body=%s; want 409", replay.Code, replay.Body.String())
	}
}
