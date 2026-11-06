package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// This test runs against the isolated PostgreSQL instance used by CI. It is
// skipped for ordinary unit-test runs where TESTAGRAM_TEST_DATABASE_URL is not
// configured. Every fixture is rolled back, including replication-outbox rows.
func TestSocialPostVisibilityPostgresIntegration(t *testing.T) {
	databaseURL := os.Getenv("TESTAGRAM_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TESTAGRAM_TEST_DATABASE_URL is not configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect to PostgreSQL integration database: %v", err)
	}
	defer conn.Close(context.Background())

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin fixture transaction: %v", err)
	}
	defer tx.Rollback(context.Background())

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var viewerID, authorID, protectedAuthorID string
	for _, fixture := range []struct {
		username string
		target   *string
	}{
		{"ci_viewer_" + suffix, &viewerID},
		{"ci_author_" + suffix, &authorID},
		{"ci_protected_" + suffix, &protectedAuthorID},
	} {
		if err := tx.QueryRow(ctx, `INSERT INTO testagram_local.accounts(user_id,username,protected_account)
			VALUES(gen_random_uuid(),$1,$2) RETURNING user_id::text`,
			fixture.username, fixture.target == &protectedAuthorID).Scan(fixture.target); err != nil {
			t.Fatalf("insert account %s: %v", fixture.username, err)
		}
	}

	var publicPostID, followersPostID, protectedPostID, privatePostID string
	for _, fixture := range []struct {
		authorID   string
		visibility string
		target     *string
	}{
		{authorID, "public", &publicPostID},
		{authorID, "followers", &followersPostID},
		{protectedAuthorID, "public", &protectedPostID},
		{authorID, "private", &privatePostID},
	} {
		if err := tx.QueryRow(ctx, `INSERT INTO testagram_local.posts(author_id,text,visibility)
			VALUES($1,$2,$3) RETURNING id::text`,
			fixture.authorID, "visibility integration "+suffix, fixture.visibility).Scan(fixture.target); err != nil {
			t.Fatalf("insert %s post: %v", fixture.visibility, err)
		}
	}

	assertVisible := func(name, viewer, post string, want bool) {
		t.Helper()
		_, err := socialCanViewPostWith(ctx, tx, viewer, post)
		got := err == nil
		if errors.Is(err, pgx.ErrNoRows) {
			got = false
		} else if err != nil {
			t.Fatalf("%s: visibility query failed: %v", name, err)
		}
		if got != want {
			t.Errorf("%s: visible=%v, want %v", name, got, want)
		}
	}

	assertVisible("public post visible to another active account", viewerID, publicPostID, true)
	assertVisible("private post hidden from another account", viewerID, privatePostID, false)
	assertVisible("author can view own private post", authorID, privatePostID, true)
	assertVisible("followers-only post hidden without accepted follow", viewerID, followersPostID, false)

	if _, err := tx.Exec(ctx, `INSERT INTO testagram_local.follows(follower_id,following_id,status)
		VALUES($1,$2,'accepted')`, viewerID, authorID); err != nil {
		t.Fatalf("insert accepted follow: %v", err)
	}
	assertVisible("followers-only post visible after accepted follow", viewerID, followersPostID, true)

	if _, err := tx.Exec(ctx, `INSERT INTO testagram_local.follows(follower_id,following_id,status)
		VALUES($1,$2,'pending')`, viewerID, protectedAuthorID); err != nil {
		t.Fatalf("insert pending protected-account follow: %v", err)
	}
	assertVisible("protected author's public post hidden while follow is pending", viewerID, protectedPostID, false)

	if _, err := tx.Exec(ctx, `UPDATE testagram_local.follows SET status='accepted'
		WHERE follower_id=$1 AND following_id=$2`, viewerID, protectedAuthorID); err != nil {
		t.Fatalf("accept protected-account follow: %v", err)
	}
	var protectedFollowerCount int64
	if err := tx.QueryRow(ctx, `SELECT follower_count FROM testagram_local.accounts WHERE user_id=$1`, protectedAuthorID).Scan(&protectedFollowerCount); err != nil {
		t.Fatalf("read protected account follower count: %v", err)
	}
	if protectedFollowerCount != 1 {
		t.Fatalf("pending-to-accepted follow transition produced follower_count=%d, want 1", protectedFollowerCount)
	}
	assertVisible("protected author's post visible after follow acceptance", viewerID, protectedPostID, true)

	if _, err := tx.Exec(ctx, `INSERT INTO testagram_local.blocks(blocker_id,blocked_id)
		VALUES($1,$2)`, authorID, viewerID); err != nil {
		t.Fatalf("insert block: %v", err)
	}
	assertVisible("block hides author's public post", viewerID, publicPostID, false)
	assertVisible("block hides author's followers-only post", viewerID, followersPostID, false)

	if _, err := tx.Exec(ctx, `UPDATE testagram_local.accounts SET account_status='suspended' WHERE user_id=$1`, viewerID); err != nil {
		t.Fatalf("suspend viewer fixture: %v", err)
	}
	assertVisible("suspended viewer cannot read a public post", viewerID, publicPostID, false)
}
