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
	t.Setenv("TESTAGRAM_WEBAUTHN_RPID", rpid)

	var requesterID, approverOneID, approverTwoID, targetID string
	if err := pool.QueryRow(ctx, "SELECT gen_random_uuid()::text,gen_random_uuid()::text,gen_random_uuid()::text,gen_random_uuid()::text").
		Scan(&requesterID, &approverOneID, &approverTwoID, &targetID); err != nil {
		t.Fatalf("generate fixture IDs: %v", err)
	}
	targetEmail := "staff-invite-" + targetID[:8] + "@example.test"
	owners := []string{requesterID, approverOneID, approverTwoID}
	requesterEmail := ""
	for index, ownerID := range owners {
		email := fmt.Sprintf("staff-invite-owner-%d-%s@example.test", index, ownerID[:8])
		if index == 0 { requesterEmail = email }
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
			INSERT INTO testagram_auth.staff_webauthn_users(rpid,user_id,user_handle)
			VALUES($1,$2::uuid,digest($2::text,'sha256'))
		`, rpid, ownerID); err != nil {
			t.Fatalf("insert owner WebAuthn user handle: %v", err)
		}
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

	admin := testApp(t)
	admin.database = pool
	requesterCredentialID := []byte("native-invitation-webauthn-credential-" + requesterID)
	requesterCookie := nativeStaffInvitationSessionCookie(t, ctx, pool, admin, requesterID, requesterEmail, requesterCredentialID, true)
	requestBody, _ := json.Marshal(map[string]string{
		"email": targetEmail, "role": "engineer",
		"reason": "Integration test invitation with verified mailbox",
	})
	requestRequest := httptest.NewRequest(http.MethodPost, "https://ops.example.test/api/staff/invite", bytes.NewReader(requestBody))
	requestRequest.AddCookie(requesterCookie)
	requestResponse := httptest.NewRecorder()
	admin.handleInvite(requestResponse, requestRequest)
	if requestResponse.Code != http.StatusCreated {
		t.Fatalf("request invitation status=%d body=%s", requestResponse.Code, requestResponse.Body.String())
	}
	var requestResult struct { ID string `json:"id"` }
	if err := json.Unmarshal(requestResponse.Body.Bytes(), &requestResult); err != nil || requestResult.ID == "" {
		t.Fatalf("decode invitation request response: id=%q err=%v body=%s", requestResult.ID, err, requestResponse.Body.String())
	}
	requestID := requestResult.ID

	for index, approverID := range []string{approverOneID, approverTwoID} {
		credentialID := []byte("native-invitation-webauthn-credential-" + approverID)
		approverEmail := fmt.Sprintf("staff-invite-owner-%d-%s@example.test", index+1, approverID[:8])
		approverCookie := nativeStaffInvitationSessionCookie(t, ctx, pool, admin, approverID, approverEmail, credentialID, true)
		decisionBody, _ := json.Marshal(map[string]string{
			"decision": "approved", "reason": "Independent owner approval for invitation",
		})
		decisionRequest := httptest.NewRequest(http.MethodPost, "https://ops.example.test/api/admin/sensitive-actions/"+requestID+"/decision", bytes.NewReader(decisionBody))
		decisionRequest.SetPathValue("id", requestID)
		decisionRequest.AddCookie(approverCookie)
		decisionResponse := httptest.NewRecorder()
		admin.handleSensitiveActionDecision(decisionResponse, decisionRequest)
		if decisionResponse.Code != http.StatusOK {
			t.Fatalf("owner approval %d status=%d body=%s", index+1, decisionResponse.Code, decisionResponse.Body.String())
		}
		var decisionResult struct { Status string `json:"status"`; ApprovalCount int `json:"approval_count"` }
		if err := json.Unmarshal(decisionResponse.Body.Bytes(), &decisionResult); err != nil {
			t.Fatalf("decode owner approval %d response: %v", index+1, err)
		}
		if index == 0 && (decisionResult.Status != "pending" || decisionResult.ApprovalCount != 1) {
			t.Fatalf("first approval state=%s count=%d; want pending/1", decisionResult.Status, decisionResult.ApprovalCount)
		}
		if index == 1 && (decisionResult.Status != "approved" || decisionResult.ApprovalCount != 2) {
			t.Fatalf("second approval state=%s count=%d; want approved/2", decisionResult.Status, decisionResult.ApprovalCount)
		}
		if index == 0 {
			var prematureInvitations, prematureMail int
			if err := pool.QueryRow(ctx, "SELECT count(*) FROM testagram_auth.native_staff_invitations WHERE request_id=$1::uuid", requestID).Scan(&prematureInvitations); err != nil {
				t.Fatal(err)
			}
			fingerprintMAC := hmac.New(sha256.New, mailKey)
			_, _ = fingerprintMAC.Write([]byte(strings.ToLower(targetEmail)))
			if err := pool.QueryRow(ctx, "SELECT count(*) FROM testagram_mail.outbox WHERE recipient_fingerprint=$1", fingerprintMAC.Sum(nil)).Scan(&prematureMail); err != nil {
				t.Fatal(err)
			}
			if prematureInvitations != 0 || prematureMail != 0 {
				t.Fatalf("first approval created invitation=%d mail=%d; no side effects are allowed before the second approval", prematureInvitations, prematureMail)
			}
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
	pending, err := admin.hasPendingStaffInvitation(ctx, targetID, targetEmail)
	if err != nil || !pending {
		t.Fatalf("approved invitation was not exposed to the exact verified invitee: pending=%v err=%v", pending, err)
	}
	wrongEmailPending, err := admin.hasPendingStaffInvitation(ctx, targetID, "wrong-"+targetEmail)
	if err != nil || wrongEmailPending {
		t.Fatalf("invitation sign-in gate accepted a different email: pending=%v err=%v", wrongEmailPending, err)
	}

	var lifecycleEvents int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM testagram_auth.native_staff_lifecycle_events
		WHERE request_id=$1::uuid AND action='invitation_created'
	`, requestID).Scan(&lifecycleEvents); err != nil || lifecycleEvents != 1 {
		t.Fatalf("invitation creation audit events=%d err=%v; want exactly one", lifecycleEvents, err)
	}

	wrongAccountCookie := nativeStaffInvitationSessionCookie(t, ctx, pool, admin, approverOneID, "staff-invite-owner-1-"+approverOneID[:8]+"@example.test", nil, false)
	wrongAccountRequest := httptest.NewRequest(http.MethodPost, "https://ops.example.test/api/staff/invitations/accept", bytes.NewReader(bodyForInvitationToken(token)))
	wrongAccountRequest.AddCookie(wrongAccountCookie)
	wrongAccountResponse := httptest.NewRecorder()
	admin.handleAcceptStaffInvitation(wrongAccountResponse, wrongAccountRequest)
	if wrongAccountResponse.Code != http.StatusConflict {
		t.Fatalf("wrong-account invitation acceptance status=%d body=%s; want 409", wrongAccountResponse.Code, wrongAccountResponse.Body.String())
	}

	cookie := nativeStaffInvitationSessionCookie(t, ctx, pool, admin, targetID, targetEmail, nil, false)
	body := bodyForInvitationToken(token)
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
	pending, err = admin.hasPendingStaffInvitation(ctx, targetID, targetEmail)
	if err != nil || pending {
		t.Fatalf("accepted invitation remained eligible for pending-invitation sign-in: pending=%v err=%v", pending, err)
	}

	replay := httptest.NewRecorder()
	replayRequest := httptest.NewRequest(http.MethodPost, "https://ops.example.test/api/staff/invitations/accept", bytes.NewReader(body))
	replayRequest.AddCookie(cookie)
	admin.handleAcceptStaffInvitation(replay, replayRequest)
	if replay.Code != http.StatusConflict {
		t.Fatalf("replayed invitation status=%d body=%s; want 409", replay.Code, replay.Body.String())
	}
}


func nativeStaffInvitationSessionCookie(t *testing.T, ctx context.Context, pool *pgxpool.Pool, admin *app, userID, email string, credentialID []byte, elevated bool) *http.Cookie {
	t.Helper()
	var tokenVersion int64
	if err := pool.QueryRow(ctx, "SELECT token_version FROM testagram_auth.identities WHERE user_id=$1::uuid", userID).Scan(&tokenVersion); err != nil {
		t.Fatalf("read native session token version: %v", err)
	}
	var sessionID string
	if err := pool.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&sessionID); err != nil {
		t.Fatalf("generate native session ID: %v", err)
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
	`, sessionID, userID, refreshHash[:], tokenVersion); err != nil {
		t.Fatalf("create native session: %v", err)
	}
	now := time.Now().UTC().Unix()
	claims := nativeJWTClaims{
		Subject: userID, Email: email, Role: "authenticated",
		Issuer: "testagram-auth", Audience: "testagram-api",
		IssuedAt: now, ExpiresAt: now + 600, TokenVersion: tokenVersion, SessionID: sessionID,
	}
	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	claimsJSON, _ := json.Marshal(claims)
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claimsJSON)
	mac := hmac.New(sha256.New, []byte(os.Getenv("TESTAGRAM_AUTH_JWT_SECRET")))
	_, _ = mac.Write([]byte(unsigned))
	accessToken := unsigned + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	elevatedToken := ""
	if elevated {
		if len(credentialID) == 0 {
			t.Fatal("credential-bound test elevation requires an authenticator ID")
		}
		var err error
		elevatedToken, _, err = admin.mintStaffElevation(authUser{ID: userID}, accessToken, credentialID)
		if err != nil {
			t.Fatalf("mint native staff WebAuthn elevation: %v", err)
		}
	}
	recorder := httptest.NewRecorder()
	if err := admin.setSession(recorder, session{
		AccessToken: accessToken, RefreshToken: refreshToken, ElevatedToken: elevatedToken,
		AccessExpiresAt: now + 600, RefreshExpiresAt: now + 86400,
	}); err != nil {
		t.Fatalf("create encrypted native session cookie: %v", err)
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected one encrypted native session cookie, got %d", len(cookies))
	}
	return cookies[0]
}

func bodyForInvitationToken(token string) []byte {
	body, _ := json.Marshal(map[string]string{"token": token})
	return body
}
