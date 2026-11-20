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

func TestIdentityReviewWebAuthnReadinessRequiresIndependentCredentialedStaff(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for native staff WebAuthn readiness integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	t.Setenv("TESTAGRAM_AUTH_JWT_SECRET", strings.Repeat("testagram-webauthn-readiness-key-", 2))
	t.Setenv("TESTAGRAM_WEBAUTHN_RPID", "admin.testagram.example")
	t.Setenv("TESTAGRAM_WEBAUTHN_ORIGINS", "https://admin.testagram.example")
	readinessToken := strings.Repeat("testagram-webauthn-readiness-shared-", 2)
	t.Setenv("TESTAGRAM_WEBAUTHN_READINESS_TOKEN", readinessToken)
	adminReady := true
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+readinessToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"identity_review_ready": adminReady,
			"credentialed_reviewers": 2,
			"credentialed_approvers": 2,
		})
	}))
	defer admin.Close()
	t.Setenv("TESTAGRAM_ADMIN_CONSOLE_READINESS_URL", admin.URL)

	suffix := fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	var users [4]string
	if err := db.QueryRow(ctx, "SELECT gen_random_uuid()::text,gen_random_uuid()::text,gen_random_uuid()::text,gen_random_uuid()::text").
		Scan(&users[0], &users[1], &users[2], &users[3]); err != nil {
		t.Fatal(err)
	}
	var grantor string
	if err := db.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&grantor); err != nil {
		t.Fatal(err)
	}
	for i, userID := range users {
		email := fmt.Sprintf("webauthn-readiness-%d-%s@example.test", i+1, suffix)
		username := fmt.Sprintf("wr%d_%s", i+1, suffix[len(suffix)-12:])
		if _, err := db.Exec(ctx, `
			INSERT INTO testagram_auth.identities(user_id,email,username,password_hash,email_verified_at)
			VALUES($1::uuid,$2,$3,'ci-only-invalid-password-hash',now())
		`, userID, email, username); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `
			INSERT INTO testagram_auth.staff_principals(user_id,enabled,mfa_enrolled_at,enabled_at,enabled_by)
			VALUES($1::uuid,TRUE,now(),now(),$2::uuid)
		`, userID, grantor); err != nil {
			t.Fatal(err)
		}
		role := "identity_reviewer"
		if i >= 2 {
			role = "administrator"
		}
		if _, err := db.Exec(ctx, `
			INSERT INTO testagram_auth.staff_role_grants(user_id,role,granted_by)
			VALUES($1::uuid,$2,$3::uuid)
		`, userID, role, grantor); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `
			INSERT INTO testagram_auth.staff_webauthn_users(rpid,user_id,user_handle)
			VALUES($1,$2::uuid,decode(md5($2),'hex'))
		`, "admin.testagram.example", userID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `
			INSERT INTO testagram_auth.staff_webauthn_credentials
			  (rpid,user_id,credential_id,credential,user_verified,clone_warning)
			VALUES($1,$2::uuid,decode(md5($2),'hex'),decode(md5($2||'credential'),'hex'),TRUE,FALSE)
		`, "admin.testagram.example", userID); err != nil {
			t.Fatal(err)
		}
	}

	if !identityReviewWebAuthnPrerequisitesReady(ctx, db) {
		t.Fatal("readiness rejected two independent verified reviewers and two independent verified approvers")
	}
	adminReady = false
	if identityReviewWebAuthnPrerequisitesReady(ctx, db) {
		t.Fatal("Backend accepted an Admin Console readiness attestation that was not ready")
	}
	adminReady = true
	if _, err := db.Exec(ctx, `
		UPDATE testagram_auth.staff_webauthn_credentials
		SET revoked_at=now()
		WHERE user_id=$1::uuid
	`, users[3]); err != nil {
		t.Fatal(err)
	}
	if identityReviewWebAuthnPrerequisitesReady(ctx, db) {
		t.Fatal("readiness remained true after an approver credential was revoked")
	}
	t.Setenv("TESTAGRAM_WEBAUTHN_ORIGINS", "http://admin.testagram.example")
	if identityReviewWebAuthnPrerequisitesReady(ctx, db) {
		t.Fatal("readiness accepted a non-HTTPS production WebAuthn origin")
	}
}
