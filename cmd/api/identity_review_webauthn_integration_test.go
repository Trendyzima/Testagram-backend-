package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/nativeauth"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAdminConsoleElevationTokenAcceptedByNativeIdentityReviewer(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for Admin Console-to-Backend MFA integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	secret := strings.Repeat("testagram-cross-service-mfa-integration-key-", 2)
	t.Setenv("TESTAGRAM_AUTH_JWT_SECRET", secret)
	t.Setenv("TESTAGRAM_AUTH_PROVIDER", "native")
	t.Setenv("TESTAGRAM_WEBAUTHN_RPID", "ops.example.test")
	t.Setenv("TESTAGRAM_WEBAUTHN_ORIGINS", "https://ops.example.test")

	suffix := fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	email := "staff-mfa-" + suffix + "@example.test"
	username := "sm_" + suffix[len(suffix)-16:]
	password := "NativeStaff-MFA-Integration-Password-2026!"
	passwordHash, err := authPasswordHash(password)
	if err != nil {
		t.Fatal(err)
	}
	var userID, grantorID string
	if err := db.QueryRow(ctx, "SELECT gen_random_uuid()::text,gen_random_uuid()::text").Scan(&userID, &grantorID); err != nil {
		t.Fatal(err)
	}
	grantorEmail := "staff-grantor-" + suffix + "@example.test"
	grantorUsername := "sg_" + suffix[len(suffix)-16:]
	if _, err := db.Exec(ctx, `
		INSERT INTO testagram_auth.identities(user_id,email,username,password_hash,email_verified_at)
		VALUES($1::uuid,$2,$3,$4,now()),
		      ($5::uuid,$6,$7,'ci-only-invalid-password-hash',now())
	`, userID, email, username, passwordHash, grantorID, grantorEmail, grantorUsername); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO testagram_local.accounts(user_id,username,display_name)
		VALUES($1::uuid,$2,'Native MFA integration')
	`, userID, username); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO testagram_auth.staff_principals(user_id,enabled,mfa_enrolled_at,enabled_at,enabled_by)
		VALUES($1::uuid,TRUE,now(),now(),$2::uuid)
	`, userID, grantorID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO testagram_auth.staff_role_grants(user_id,role,granted_by)
		VALUES($1::uuid,'identity_reviewer',$2::uuid)
	`, userID, grantorID); err != nil {
		t.Fatal(err)
	}

	credentialID := []byte("testagram-webauthn-credential-" + suffix)
	if _, err := db.Exec(ctx, `
		INSERT INTO testagram_auth.staff_webauthn_users(rpid,user_id,user_handle)
		VALUES('ops.example.test',$1::uuid,decode(md5($1::text),'hex'))
	`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO testagram_auth.staff_webauthn_credentials
		  (rpid,user_id,credential_id,credential,user_verified,clone_warning)
		VALUES('ops.example.test',$1::uuid,$2,'ci-only-encrypted-credential',TRUE,FALSE)
	`, userID, credentialID); err != nil {
		t.Fatal(err)
	}

	router := (&service{db: db}).routes()
	request := func(method, path string, payload any, bearer string) *httptest.ResponseRecorder {
		t.Helper()
		var body []byte
		if payload != nil {
			body, _ = json.Marshal(payload)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.RemoteAddr = "192.0.2.220:4545"
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	login := request(http.MethodPost, "/api/auth/login", map[string]string{"email": email, "password": password}, "")
	if login.Code != http.StatusOK {
		t.Fatalf("native staff login status=%d body=%s", login.Code, login.Body.String())
	}
	var envelope struct {
		OK   bool                `json:"ok"`
		Data nativeSessionTokens `json:"data"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &envelope); err != nil || !envelope.OK || envelope.Data.AccessToken == "" {
		t.Fatalf("native staff login returned invalid session: err=%v body=%s", err, login.Body.String())
	}

	elevated, _, err := nativeauth.MintStaffElevationToken(envelope.Data.AccessToken, userID, credentialID, []byte(secret), time.Now().UTC())
	if err != nil {
		t.Fatalf("Admin Console shared WebAuthn elevation signer rejected native session: %v", err)
	}
	reviewerReq := httptest.NewRequest(http.MethodGet, "/api/admin/identity/review-queue", nil)
	reviewerReq.Header.Set("Authorization", "Bearer "+elevated)
	reviewer, status, err := (&service{db: db}).identityReviewer(reviewerReq)
	if err != nil || status != http.StatusOK || reviewer.ID != userID {
		t.Fatalf("Backend rejected Admin Console elevation token: reviewer=%#v status=%d err=%v", reviewer, status, err)
	}

	// The exact authenticator used for step-up must remain active for every
	// privileged request. Revocation invalidates an outstanding elevation
	// immediately rather than waiting for its five-minute expiry.
	if _, err := db.Exec(ctx, `
		UPDATE testagram_auth.staff_webauthn_credentials
		SET revoked_at=now()
		WHERE user_id=$1::uuid AND credential_id=$2
	`, userID, credentialID); err != nil {
		t.Fatal(err)
	}
	if _, status, err := (&service{db: db}).identityReviewer(reviewerReq); err == nil || status != http.StatusForbidden {
		t.Fatalf("revoked WebAuthn credential retained MFA elevation: status=%d err=%v", status, err)
	}
	if _, err := db.Exec(ctx, `
		UPDATE testagram_auth.staff_webauthn_credentials
		SET revoked_at=NULL
		WHERE user_id=$1::uuid AND credential_id=$2
	`, userID, credentialID); err != nil {
		t.Fatal(err)
	}
	if _, status, err := (&service{db: db}).identityReviewer(reviewerReq); err != nil || status != http.StatusOK {
		t.Fatalf("restored active WebAuthn credential did not restore the valid elevation: status=%d err=%v", status, err)
	}

	// An approver-only administrator must not use the evidence-review API.
	if _, err := db.Exec(ctx, `
		INSERT INTO testagram_auth.staff_principals(user_id,enabled,mfa_enrolled_at,enabled_at,enabled_by)
		VALUES($1::uuid,TRUE,now(),now(),$2::uuid)
	`, grantorID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO testagram_auth.staff_role_grants(user_id,role,granted_by)
		VALUES($1::uuid,'administrator',$2::uuid)
	`, grantorID, userID); err != nil {
		t.Fatal(err)
	}
	approverSession, err := issueNativeSession(ctx, db, grantorID, grantorEmail, 1, "ci-admin-console", "192.0.2.221")
	if err != nil {
		t.Fatalf("create native approver-only session: %v", err)
	}
	approverElevation, _, err := nativeauth.MintStaffElevationToken(approverSession.AccessToken, grantorID, []byte("approver-credential-"+suffix), []byte(secret), time.Now().UTC())
	if err != nil {
		t.Fatalf("mint approver-only MFA elevation: %v", err)
	}
	approverReq := httptest.NewRequest(http.MethodGet, "/api/admin/identity/review-queue", nil)
	approverReq.Header.Set("Authorization", "Bearer "+approverElevation)
	if _, status, err := (&service{db: db}).identityReviewer(approverReq); err == nil || status != http.StatusForbidden {
		t.Fatalf("approver-only administrator was allowed into evidence-review API: status=%d err=%v", status, err)
	}

	logout := request(http.MethodPost, "/api/auth/logout", map[string]string{}, envelope.Data.AccessToken)
	if logout.Code != http.StatusOK {
		t.Fatalf("native session logout status=%d body=%s", logout.Code, logout.Body.String())
	}
	_, status, err = (&service{db: db}).identityReviewer(reviewerReq)
	if err == nil || status != http.StatusUnauthorized {
		t.Fatalf("elevation token survived underlying native session revocation: status=%d err=%v", status, err)
	}
}
