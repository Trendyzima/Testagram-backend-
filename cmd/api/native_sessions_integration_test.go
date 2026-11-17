package main

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func nativeSessionTestSetup(t *testing.T) (*pgxpool.Pool, *service, string, string, int64) {
	t.Helper()
	db := nativeRecoveryTestDatabase(t)
	userID, email, version := nativeRecoveryTestIdentity(t, db)
	t.Setenv("TESTAGRAM_AUTH_JWT_SECRET", "native-session-integration-secret-0123456789abcdef")
	return db, &service{db: db}, userID, email, version
}

func TestNativeSessionRefreshRotationAndReuseRevokesFamily(t *testing.T) {
	db, svc, userID, email, version := nativeSessionTestSetup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	initial, err := issueNativeSession(ctx, db, userID, email, version, "session-integration", "192.0.2.21")
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := svc.rotateNativeRefresh(ctx, initial.RefreshToken, "session-integration", "192.0.2.21")
	if err != nil {
		t.Fatalf("first refresh failed: %v", err)
	}
	if rotated.RefreshToken == "" || rotated.RefreshToken == initial.RefreshToken ||
		rotated.AccessToken == "" || rotated.AccessToken == initial.AccessToken {
		t.Fatal("refresh did not issue a new access and refresh token")
	}
	if _, err := svc.rotateNativeRefresh(ctx, initial.RefreshToken, "session-integration", "192.0.2.21"); err == nil {
		t.Fatal("reused refresh token was accepted")
	}
	if _, status, err := verifyNativeAccessToken(ctx, db, rotated.AccessToken); err == nil || status != 401 {
		t.Fatalf("refresh-token reuse did not revoke the rotated session: status=%d err=%v", status, err)
	}
	if _, err := svc.rotateNativeRefresh(ctx, rotated.RefreshToken, "session-integration", "192.0.2.21"); err == nil {
		t.Fatal("replacement refresh token remained usable after family reuse detection")
	}
}

func TestNativeSessionLogoutRevokesCurrentSession(t *testing.T) {
	db, svc, userID, email, version := nativeSessionTestSetup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	issued, err := issueNativeSession(ctx, db, userID, email, version, "session-logout", "192.0.2.22")
	if err != nil {
		t.Fatal(err)
	}
	claims, status, err := verifyNativeAccessToken(ctx, db, issued.AccessToken)
	if err != nil || status != 200 {
		t.Fatalf("new access token was not accepted: status=%d err=%v", status, err)
	}
	if err := svc.revokeNativeSession(ctx, claims, false, "192.0.2.22"); err != nil {
		t.Fatalf("logout failed: %v", err)
	}
	if _, status, err := verifyNativeAccessToken(ctx, db, issued.AccessToken); err == nil || status != 401 {
		t.Fatalf("logout left access token active: status=%d err=%v", status, err)
	}
	if _, err := svc.rotateNativeRefresh(ctx, issued.RefreshToken, "session-logout", "192.0.2.22"); err == nil {
		t.Fatal("logout left refresh token active")
	}
}

func TestNativeSessionLogoutAllRevokesEverySession(t *testing.T) {
	db, svc, userID, email, version := nativeSessionTestSetup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	first, err := issueNativeSession(ctx, db, userID, email, version, "device-one", "192.0.2.23")
	if err != nil {
		t.Fatal(err)
	}
	second, err := issueNativeSession(ctx, db, userID, email, version, "device-two", "192.0.2.24")
	if err != nil {
		t.Fatal(err)
	}
	claims, status, err := verifyNativeAccessToken(ctx, db, first.AccessToken)
	if err != nil || status != 200 {
		t.Fatalf("first session was not active: status=%d err=%v", status, err)
	}
	if err := svc.revokeNativeSession(ctx, claims, true, "192.0.2.23"); err != nil {
		t.Fatalf("logout-all failed: %v", err)
	}
	for name, issued := range map[string]nativeSessionTokens{"first": first, "second": second} {
		if _, status, err := verifyNativeAccessToken(ctx, db, issued.AccessToken); err == nil || status != 401 {
			t.Errorf("%s access token survived logout-all: status=%d err=%v", name, status, err)
		}
		if _, err := svc.rotateNativeRefresh(ctx, issued.RefreshToken, name, "192.0.2.25"); err == nil {
			t.Errorf("%s refresh token survived logout-all", name)
		}
	}
	var nextVersion int64
	if err := db.QueryRow(ctx, "SELECT token_version FROM testagram_auth.identities WHERE user_id=$1::uuid", userID).Scan(&nextVersion); err != nil {
		t.Fatal(err)
	}
	if nextVersion != version+1 {
		t.Fatalf("logout-all token version=%d; want %d", nextVersion, version+1)
	}
}

func TestNativeSessionConcurrentRefreshReuseRevokesFamily(t *testing.T) {
	db, svc, userID, email, version := nativeSessionTestSetup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	issued, err := issueNativeSession(ctx, db, userID, email, version, "concurrent-refresh", "192.0.2.26")
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		tokens nativeSessionTokens
		err    error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tokens, err := svc.rotateNativeRefresh(ctx, issued.RefreshToken, "concurrent-refresh", "192.0.2.26")
			results <- result{tokens: tokens, err: err}
		}()
	}
	wg.Wait()
	close(results)

	successes := 0
	var successful nativeSessionTokens
	for result := range results {
		if result.err == nil {
			successes++
			successful = result.tokens
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent use of one refresh token produced %d successful rotations; want exactly one", successes)
	}
	if _, status, err := verifyNativeAccessToken(ctx, db, successful.AccessToken); err == nil || status != 401 {
		t.Fatalf("concurrent refresh replay did not revoke the token family: status=%d err=%v", status, err)
	}
	if _, err := svc.rotateNativeRefresh(ctx, successful.RefreshToken, "concurrent-refresh", "192.0.2.26"); err == nil {
		t.Fatal(fmt.Sprintf("replacement token remained usable after concurrent replay"))
	}
}
