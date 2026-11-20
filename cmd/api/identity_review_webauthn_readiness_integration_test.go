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
	readinessToken := strings.Repeat("testagram-webauthn-readiness-shared-", 2)
	t.Setenv("TESTAGRAM_WEBAUTHN_READINESS_TOKEN", readinessToken)
	adminReady := true
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+readinessToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"identity_review_ready":  adminReady,
			"credentialed_staff":     4,
			"credentialed_reviewers": 2,
			"credentialed_approvers": 2,
		})
	}))
	defer admin.Close()
	t.Setenv("TESTAGRAM_ADMIN_CONSOLE_READINESS_URL", admin.URL+"/internal/webauthn/readiness")

	suffix := fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	// Scope this test's credentials to a unique RP ID so unrelated staff
	// credentials from other PostgreSQL fixtures cannot affect independence counts.
	rpid := "admin-" + suffix + ".testagram.example"
	t.Setenv("TESTAGRAM_WEBAUTHN_RPID", rpid)
	t.Setenv("TESTAGRAM_WEBAUTHN_ORIGINS", "https://"+rpid)
	var users [4]string
	if err := db.QueryRow(ctx, "SELECT gen_random_uuid()::text,gen_random_uuid()::text,gen_random_uuid()::text,gen_random_uuid()::text").
		Scan(&users[0], &users[1], &users[2], &users[3]); err != nil {
		t.Fatal(err)
	}
	var grantor string
	if err := db.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&grantor); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO testagram_auth.identities(user_id,email,username,password_hash,email_verified_at)
		VALUES($1::uuid,$2,$3,'ci-only-invalid-password-hash',now())
	`, grantor, fmt.Sprintf("webauthn-grantor-%s@example.test", suffix),
		"wgrantor_"+suffix[len(suffix)-12:]); err != nil {
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
			VALUES($1,$2::uuid,decode(md5($2::text),'hex'))
		`, rpid, userID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `
			INSERT INTO testagram_auth.staff_webauthn_credentials
			  (rpid,user_id,credential_id,credential,user_verified,clone_warning)
			VALUES($1,$2::uuid,decode(md5($2::text),'hex'),decode(md5($2::text||'credential'),'hex'),TRUE,FALSE)
		`, rpid, userID); err != nil {
			t.Fatal(err)
		}
	}

	if !identityReviewWebAuthnPrerequisitesReady(ctx, db) {
		t.Fatal("readiness rejected two independent verified reviewers and two independent verified approvers")
	}
	// A staff member holding both roles must not count toward both independent
	// credential groups. Four credentialed people still exist, but only one
	// reviewer is exclusive when users[0] is dual-roled.
	if _, err := db.Exec(ctx, `
		INSERT INTO testagram_auth.staff_role_grants(user_id,role,granted_by)
		VALUES($1::uuid,'administrator',$2::uuid)
	`, users[0], grantor); err != nil {
		t.Fatal(err)
	}
	if identityReviewWebAuthnPrerequisitesReady(ctx, db) {
		t.Fatal("readiness counted a dual-role staff member in both independent credential groups")
	}
	if _, err := db.Exec(ctx, `
		DELETE FROM testagram_auth.staff_role_grants
		WHERE user_id=$1::uuid AND role='administrator'
	`, users[0]); err != nil {
		t.Fatal(err)
	}
	if !identityReviewWebAuthnPrerequisitesReady(ctx, db) {
		t.Fatal("readiness did not recover after removing the overlapping role")
	}
	if _, err := db.Exec(ctx, `
		UPDATE testagram_auth.staff_role_grants
		SET role='administrator'
		WHERE user_id IN ($1::uuid,$2::uuid)
	`, users[0], users[1]); err != nil {
		t.Fatal(err)
	}
	if identityReviewWebAuthnPrerequisitesReady(ctx, db) {
		t.Fatal("readiness accepted overlapping administrator-only reviewer and approver groups")
	}
	if _, err := db.Exec(ctx, `
		UPDATE testagram_auth.staff_role_grants
		SET role='identity_reviewer'
		WHERE user_id IN ($1::uuid,$2::uuid)
	`, users[0], users[1]); err != nil {
		t.Fatal(err)
	}
	adminReady = false
	if identityReviewWebAuthnPrerequisitesReady(ctx, db) {
		t.Fatal("Backend accepted an Admin Console readiness attestation that was not ready")
	}
	adminReady = true
	// The shared readiness bearer must never be forwarded to an arbitrary
	// configured host, even if that host returns a syntactically valid attestation.
	t.Setenv("TESTAGRAM_ADMIN_CONSOLE_READINESS_URL", "http://attacker.example:8090/internal/webauthn/readiness")
	if identityReviewWebAuthnPrerequisitesReady(ctx, db) {
		t.Fatal("readiness forwarded its shared bearer token to an untrusted external host")
	}
	t.Setenv("TESTAGRAM_ADMIN_CONSOLE_READINESS_URL", admin.URL+"/internal/webauthn/readiness")
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
