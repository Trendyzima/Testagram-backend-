package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func nativeStaffReviewerFixture(t *testing.T, ctx context.Context, db *pgxpool.Pool) (string, string, string) {
	t.Helper()
	t.Setenv("TESTAGRAM_AUTH_JWT_SECRET", strings.Repeat("s", 64))
	passwordHash, err := authPasswordHash("native-reviewer-password-123")
	if err != nil {
		t.Fatal(err)
	}
	newIdentity := func(prefix string) (string, string) {
		t.Helper()
		var userID string
		if err := db.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&userID); err != nil {
			t.Fatal(err)
		}
		compact := strings.ReplaceAll(userID, "-", "")
		email := prefix + "-" + compact[:12] + "@example.test"
		username := prefix + "_" + compact[:12]
		if _, err := db.Exec(ctx, "INSERT INTO testagram_auth.identities(user_id,email,username,password_hash,email_verified_at,token_version) VALUES($1::uuid,$2,$3,$4,now(),1)", userID, email, username, passwordHash); err != nil {
			t.Fatal(err)
		}
		return userID, email
	}
	reviewerID, reviewerEmail := newIdentity("reviewer")
	requesterID, _ := newIdentity("requester")
	approverID, _ := newIdentity("approver")
	if _, err := db.Exec(ctx, "INSERT INTO testagram_auth.staff_principals(user_id,enabled,mfa_enrolled_at,enabled_at,enabled_by) VALUES($1::uuid,TRUE,now(),now(),$2::uuid)", reviewerID, approverID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, "INSERT INTO testagram_auth.staff_role_grants(user_id,role,granted_by) VALUES($1::uuid,'administrator',$2::uuid)", reviewerID, approverID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, "INSERT INTO testagram_auth.staff_approval_events(subject_user_id,action,role,requested_by,approved_by,requested_at,approved_at,applied_at) VALUES($1::uuid,'grant_role','administrator',$2::uuid,$3::uuid,now()-interval '1 minute',now(),now())", reviewerID, requesterID, approverID); err != nil {
		t.Fatal(err)
	}
	session, err := issueNativeSession(ctx, db, reviewerID, reviewerEmail, 1, "ci-native-reviewer", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	claims, status, err := verifyNativeAccessToken(ctx, db, session.AccessToken)
	if err != nil || status != http.StatusOK {
		t.Fatalf("verify native reviewer session status=%d err=%v", status, err)
	}
	claims.MFAAuthenticatedAt = time.Now().UTC().Unix()
	session.AccessToken, err = authSignJWTClaims(claims)
	if err != nil {
		t.Fatal(err)
	}
	return reviewerID, reviewerEmail, session.AccessToken
}

func TestIdentityReviewerHonorsNativeRoleRevocationAndMFA(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL reviewer authorization integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reviewerID, reviewerEmail, token := nativeStaffReviewerFixture(t, ctx, db)
	svc := &service{db: db}
	authorize := func() (int, error) {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/identity/review-queue", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		reviewer, status, err := svc.identityReviewer(req)
		if err == nil && (reviewer.ID != reviewerID || reviewer.Email != reviewerEmail) {
			t.Fatalf("native reviewer identity=%q email=%q; want %q / %q", reviewer.ID, reviewer.Email, reviewerID, reviewerEmail)
		}
		return status, err
	}
	if status, err := authorize(); status != http.StatusOK || err != nil {
		t.Fatalf("active native administrator reviewer status=%d err=%v; want 200", status, err)
	}
	if _, err := db.Exec(ctx, "UPDATE testagram_auth.staff_role_grants SET revoked_at=now(),revoked_by=granted_by WHERE user_id=$1::uuid AND role='administrator' AND revoked_at IS NULL", reviewerID); err != nil {
		t.Fatal(err)
	}
	if status, err := authorize(); status != http.StatusForbidden || err == nil {
		t.Fatalf("revoked native role status=%d err=%v; want 403", status, err)
	}
	if _, err := db.Exec(ctx, "UPDATE testagram_auth.staff_role_grants SET revoked_at=NULL,revoked_by=NULL WHERE user_id=$1::uuid AND role='administrator'", reviewerID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, "UPDATE testagram_auth.staff_role_grants SET role='support' WHERE user_id=$1::uuid AND role='administrator' AND revoked_at IS NULL", reviewerID); err != nil {
		t.Fatal(err)
	}
	if status, err := authorize(); status != http.StatusForbidden || err == nil {
		t.Fatalf("unsupported native staff role status=%d err=%v; want 403", status, err)
	}
	if _, err := db.Exec(ctx, "UPDATE testagram_auth.staff_role_grants SET role='administrator' WHERE user_id=$1::uuid AND role='support' AND revoked_at IS NULL", reviewerID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, "UPDATE testagram_auth.staff_principals SET enabled=FALSE WHERE user_id=$1::uuid", reviewerID); err != nil {
		t.Fatal(err)
	}
	if status, err := authorize(); status != http.StatusForbidden || err == nil {
		t.Fatalf("disabled native staff principal status=%d err=%v; want 403", status, err)
	}
	if _, err := db.Exec(ctx, "UPDATE testagram_auth.staff_principals SET enabled=TRUE,mfa_enrolled_at=NULL WHERE user_id=$1::uuid", reviewerID); err != nil {
		t.Fatal(err)
	}
	if status, err := authorize(); status != http.StatusForbidden || err == nil {
		t.Fatalf("native reviewer without enrolled MFA status=%d err=%v; want 403", status, err)
	}
}
