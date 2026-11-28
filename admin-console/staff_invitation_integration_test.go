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
	router := admin.routes()
	requesterCredentialID := []byte("native-invitation-webauthn-credential-" + requesterID)
	requesterCookie := nativeStaffInvitationSessionCookie(t, ctx, pool, admin, requesterID, requesterEmail, requesterCredentialID, true)
	requestBody, _ := json.Marshal(map[string]string{
		"email": targetEmail, "role": "engineer",
		"reason": "Integration test invitation with verified mailbox",
	})
	requestRequest := httptest.NewRequest(http.MethodPost, "https://ops.example.test/api/staff/invite", bytes.NewReader(requestBody))
	requestRequest.AddCookie(requesterCookie)
	requestRequest.Header.Set("Origin", "https://ops.example.test")
	requestResponse := httptest.NewRecorder()
	router.ServeHTTP(requestResponse, requestRequest)
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
		decisionRequest.AddCookie(approverCookie)
		decisionRequest.Header.Set("Origin", "https://ops.example.test")
		decisionResponse := httptest.NewRecorder()
		router.ServeHTTP(decisionResponse, decisionRequest)
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

	landingRequest := httptest.NewRequest(http.MethodGet, "https://ops.example.test/staff/invitations/accept?token="+url.QueryEscape(token), nil)
	landingResponse := httptest.NewRecorder()
	router.ServeHTTP(landingResponse, landingRequest)
	if landingResponse.Code != http.StatusOK || !strings.Contains(landingResponse.Body.String(), `id="accept-invitation"`) {
		t.Fatalf("invitation landing page status=%d body=%s", landingResponse.Code, landingResponse.Body.String())
	}
	if landingResponse.Header().Get("Referrer-Policy") != "no-referrer" || landingResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("invitation landing page leaks bearer token through headers: referrer=%q cache=%q", landingResponse.Header().Get("Referrer-Policy"), landingResponse.Header().Get("Cache-Control"))
	}

	wrongAccountCookie := nativeStaffInvitationSessionCookie(t, ctx, pool, admin, approverOneID, "staff-invite-owner-1-"+approverOneID[:8]+"@example.test", nil, false)
	wrongAccountRequest := httptest.NewRequest(http.MethodPost, "https://ops.example.test/api/staff/invitations/accept", bytes.NewReader(bodyForInvitationToken(token)))
	wrongAccountRequest.AddCookie(wrongAccountCookie)
	wrongAccountRequest.Header.Set("Origin", "https://ops.example.test")
	wrongAccountResponse := httptest.NewRecorder()
	router.ServeHTTP(wrongAccountResponse, wrongAccountRequest)
	if wrongAccountResponse.Code != http.StatusConflict {
		t.Fatalf("wrong-account invitation acceptance status=%d body=%s; want 409", wrongAccountResponse.Code, wrongAccountResponse.Body.String())
	}

	cookie := nativeStaffInvitationSessionCookie(t, ctx, pool, admin, targetID, targetEmail, nil, false)
	body := bodyForInvitationToken(token)
	acceptRequest := httptest.NewRequest(http.MethodPost, "https://ops.example.test/api/staff/invitations/accept", bytes.NewReader(body))
	acceptRequest.AddCookie(cookie)
	acceptRequest.Header.Set("Origin", "https://ops.example.test")
	acceptResponse := httptest.NewRecorder()
	router.ServeHTTP(acceptResponse, acceptRequest)
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
	replayRequest.Header.Set("Origin", "https://ops.example.test")
	router.ServeHTTP(replay, replayRequest)
	if replay.Code != http.StatusConflict {
		t.Fatalf("replayed invitation status=%d body=%s; want 409", replay.Code, replay.Body.String())
	}

	// Exercise revocation through the real HTTP handlers and isolated PostgreSQL.
	// This second invitation is revoked before acceptance; the first invitation
	// above has already exercised the normal acceptance and replay lifecycle.
	var revokeTargetID string
	if err := pool.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&revokeTargetID); err != nil { t.Fatal(err) }
	revokeTargetEmail := "staff-revoke-" + revokeTargetID[:8] + "@example.test"
	if _, err := pool.Exec(ctx, `
		INSERT INTO testagram_auth.identities(user_id,email,username,password_hash,email_verified_at)
		VALUES($1::uuid,$2,$3,'test-only-invalid-password-hash',now())
	`, revokeTargetID, revokeTargetEmail, "staffrevoke_"+revokeTargetID[:8]); err != nil { t.Fatalf("insert revocation target identity: %v",err) }
	revokeInviteBody,_:=json.Marshal(map[string]string{
		"email":revokeTargetEmail,"role":"support","reason":"Integration test invitation for revocation lifecycle",
	})
	revokeInviteRequest:=httptest.NewRequest(http.MethodPost,"https://ops.example.test/api/staff/invite",bytes.NewReader(revokeInviteBody))
	revokeInviteRequest.AddCookie(requesterCookie)
	revokeInviteRequest.Header.Set("Origin","https://ops.example.test")
	revokeInviteResponse:=httptest.NewRecorder()
	router.ServeHTTP(revokeInviteResponse,revokeInviteRequest)
	if revokeInviteResponse.Code!=http.StatusCreated { t.Fatalf("revocation fixture invitation request status=%d body=%s",revokeInviteResponse.Code,revokeInviteResponse.Body.String()) }
	var revokeInviteResult struct { ID string `json:"id"` }
	if err:=json.Unmarshal(revokeInviteResponse.Body.Bytes(),&revokeInviteResult); err!=nil || revokeInviteResult.ID=="" { t.Fatalf("decode revocation fixture invitation: id=%q err=%v",revokeInviteResult.ID,err) }
	for index,approverID:=range []string{approverOneID,approverTwoID} {
		approverCredentialID:=[]byte("native-invitation-webauthn-credential-"+approverID)
		approverEmail:=fmt.Sprintf("staff-invite-owner-%d-%s@example.test",index+1,approverID[:8])
		approverCookie:=nativeStaffInvitationSessionCookie(t,ctx,pool,admin,approverID,approverEmail,approverCredentialID,true)
		body,_:=json.Marshal(map[string]string{"decision":"approved","reason":"Independent owner approval for revocation fixture invitation"})
		req:=httptest.NewRequest(http.MethodPost,"https://ops.example.test/api/admin/sensitive-actions/"+revokeInviteResult.ID+"/decision",bytes.NewReader(body))
		req.AddCookie(approverCookie); req.Header.Set("Origin","https://ops.example.test")
		rec:=httptest.NewRecorder(); router.ServeHTTP(rec,req)
		if rec.Code!=http.StatusOK { t.Fatalf("revocation fixture invitation approval %d status=%d body=%s",index+1,rec.Code,rec.Body.String()) }
	}
	var revokeInvitationID string
	var revokeCiphertext,revokeNonce []byte
	revokeMAC:=hmac.New(sha256.New,mailKey); _,_=revokeMAC.Write([]byte(strings.ToLower(revokeTargetEmail)))
	if err:=pool.QueryRow(ctx,`
		SELECT ni.id::text,o.payload_ciphertext,o.payload_nonce
		FROM testagram_auth.native_staff_invitations ni
		JOIN testagram_mail.outbox o ON o.recipient_fingerprint=$2 AND o.status='pending'
		WHERE ni.request_id=$1::uuid ORDER BY o.id DESC LIMIT 1
	`,revokeInviteResult.ID,revokeMAC.Sum(nil)).Scan(&revokeInvitationID,&revokeCiphertext,&revokeNonce); err!=nil { t.Fatalf("load revocation test invitation and encrypted mail: %v",err) }
	revokePlain,err:=aead.Open(nil,revokeNonce,revokeCiphertext,[]byte("testagram-mail-v1"))
	if err!=nil { t.Fatalf("decrypt revocation test invitation mail: %v",err) }
	var revokeMail staffInvitationMail
	if err:=json.Unmarshal(revokePlain,&revokeMail); err!=nil { t.Fatalf("decode revocation test invitation mail: %v",err) }
	revokeLinkStart:=strings.Index(revokeMail.HTML,`href="`)
	if revokeLinkStart<0 { t.Fatal("revocation test mail has no acceptance link") }
	revokeLinkStart+=len(`href="`)
	revokeLinkEnd:=strings.Index(revokeMail.HTML[revokeLinkStart:],`"`)
	if revokeLinkEnd<0 { t.Fatal("revocation test acceptance link is malformed") }
	revokeURL,err:=url.Parse(revokeMail.HTML[revokeLinkStart:revokeLinkStart+revokeLinkEnd])
	if err!=nil { t.Fatalf("parse revocation test acceptance link: %v",err) }
	revokeToken:=revokeURL.Query().Get("token")
	if revokeToken=="" { t.Fatal("revocation test invitation token missing from encrypted mail") }

	revokeBody,_:=json.Marshal(map[string]string{"reason":"Invitation is no longer required by the business"})
	revokeRequest:=httptest.NewRequest(http.MethodPost,"https://ops.example.test/api/staff/invitations/"+revokeInvitationID+"/revoke",bytes.NewReader(revokeBody))
	revokeRequest.AddCookie(requesterCookie); revokeRequest.Header.Set("Origin","https://ops.example.test")
	revokeResponse:=httptest.NewRecorder(); router.ServeHTTP(revokeResponse,revokeRequest)
	if revokeResponse.Code!=http.StatusCreated { t.Fatalf("request invitation revocation status=%d body=%s",revokeResponse.Code,revokeResponse.Body.String()) }
	var revokeResult struct { ID string `json:"id"`; RequiredApprovals int `json:"required_approvals"` }
	if err:=json.Unmarshal(revokeResponse.Body.Bytes(),&revokeResult); err!=nil || revokeResult.ID=="" || revokeResult.RequiredApprovals!=2 { t.Fatalf("decode revocation request: %#v err=%v",revokeResult,err) }
	for index,approverID:=range []string{approverOneID,approverTwoID} {
		approverCredentialID:=[]byte("native-invitation-webauthn-credential-"+approverID)
		approverEmail:=fmt.Sprintf("staff-invite-owner-%d-%s@example.test",index+1,approverID[:8])
		approverCookie:=nativeStaffInvitationSessionCookie(t,ctx,pool,admin,approverID,approverEmail,approverCredentialID,true)
		body,_:=json.Marshal(map[string]string{"decision":"approved","reason":"Independent owner approval to revoke pending invitation"})
		req:=httptest.NewRequest(http.MethodPost,"https://ops.example.test/api/admin/sensitive-actions/"+revokeResult.ID+"/decision",bytes.NewReader(body))
		req.AddCookie(approverCookie); req.Header.Set("Origin","https://ops.example.test")
		rec:=httptest.NewRecorder(); router.ServeHTTP(rec,req)
		if rec.Code!=http.StatusOK { t.Fatalf("invitation revocation approval %d status=%d body=%s",index+1,rec.Code,rec.Body.String()) }
		var decision struct { Status string `json:"status"`; ApprovalCount int `json:"approval_count"` }
		if err:=json.Unmarshal(rec.Body.Bytes(),&decision); err!=nil { t.Fatalf("decode revocation approval %d: %v",index+1,err) }
		if index==0 && (decision.Status!="pending" || decision.ApprovalCount!=1) { t.Fatalf("first revocation approval state=%s count=%d",decision.Status,decision.ApprovalCount) }
		if index==1 && (decision.Status!="approved" || decision.ApprovalCount!=2) { t.Fatalf("second revocation approval state=%s count=%d",decision.Status,decision.ApprovalCount) }
	}
	var revoked,accepted bool
	if err:=pool.QueryRow(ctx,"SELECT revoked_at IS NOT NULL,accepted_at IS NOT NULL FROM testagram_auth.native_staff_invitations WHERE id=$1::uuid",revokeInvitationID).Scan(&revoked,&accepted); err!=nil || !revoked || accepted {
		t.Fatalf("revoked invitation state revoked=%v accepted=%v err=%v; want true/false",revoked,accepted,err)
	}
	var revokedEvents int
	if err:=pool.QueryRow(ctx,`SELECT count(*) FROM testagram_auth.native_staff_lifecycle_events WHERE request_id=$1::uuid AND action='invitation_revoked'`,revokeResult.ID).Scan(&revokedEvents); err!=nil || revokedEvents!=1 {
		t.Fatalf("invitation revocation audit events=%d err=%v; want exactly one",revokedEvents,err)
	}
	revokeTargetCookie:=nativeStaffInvitationSessionCookie(t,ctx,pool,admin,revokeTargetID,revokeTargetEmail,nil,false)
	revokedAccept:=httptest.NewRecorder()
	revokedAcceptRequest:=httptest.NewRequest(http.MethodPost,"https://ops.example.test/api/staff/invitations/accept",bytes.NewReader(bodyForInvitationToken(revokeToken)))
	revokedAcceptRequest.AddCookie(revokeTargetCookie); revokedAcceptRequest.Header.Set("Origin","https://ops.example.test")
	router.ServeHTTP(revokedAccept,revokedAcceptRequest)
	if revokedAccept.Code!=http.StatusConflict { t.Fatalf("revoked invitation acceptance status=%d body=%s; want 409",revokedAccept.Code,revokedAccept.Body.String()) }

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
