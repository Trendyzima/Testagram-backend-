package main

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// This focused test executes the same transactional mention helper used by
// post creation against the isolated PostgreSQL instance in CI.
func TestSocialMentionNotificationsPostgresIntegration(t *testing.T) {
	databaseURL := os.Getenv("TESTAGRAM_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TESTAGRAM_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
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

	suffix := fmt.Sprintf("%09d", time.Now().UnixNano()%1_000_000_000)
	var authorID, followerID, strangerID, protectedID string
	for _, fixture := range []struct {
		username  string
		protected bool
		target    *string
	}{
		{"ci_mention_author_" + suffix, false, &authorID},
		{"ci_mention_follower_" + suffix, false, &followerID},
		{"ci_mention_stranger_" + suffix, false, &strangerID},
		{"ci_mention_protected_" + suffix, true, &protectedID},
	} {
		if err := tx.QueryRow(ctx, `INSERT INTO testagram_local.accounts(user_id,username,protected_account)
			VALUES(gen_random_uuid(),$1,$2) RETURNING user_id::text`, fixture.username, fixture.protected).Scan(fixture.target); err != nil {
			t.Fatalf("insert account %s: %v", fixture.username, err)
		}
	}
	var publicPostID, followersPostID, privatePostID, protectedPostID, blockedPostID string
	for _, fixture := range []struct {
		author, visibility string
		target             *string
	}{
		{authorID, "public", &publicPostID},
		{authorID, "followers", &followersPostID},
		{authorID, "private", &privatePostID},
		{protectedID, "public", &protectedPostID},
		{authorID, "public", &blockedPostID},
	} {
		if err := tx.QueryRow(ctx, `INSERT INTO testagram_local.posts(author_id,text,visibility)
			VALUES($1,'mention integration fixture',$2) RETURNING id::text`, fixture.author, fixture.visibility).Scan(fixture.target); err != nil {
			t.Fatalf("insert %s mention post: %v", fixture.visibility, err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO testagram_local.follows(follower_id,following_id,status)
		VALUES($1,$2,'accepted')`, followerID, authorID); err != nil {
		t.Fatalf("insert accepted follower: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO testagram_local.blocks(blocker_id,blocked_id)
		VALUES($1,$2)`, authorID, strangerID); err != nil {
		t.Fatalf("insert blocked recipient: %v", err)
	}

	publicText := "Hello @" + "ci_mention_follower_" + suffix + " and @" + "ci_mention_stranger_" + suffix
	for attempt := 0; attempt < 2; attempt++ {
		if err := createSocialMentionNotifications(ctx, tx, authorID, publicPostID, "public", publicText); err != nil {
			t.Fatalf("create public mention notifications (attempt %d): %v", attempt+1, err)
		}
	}
	assertCount := func(label, recipient, post string, want int) {
		t.Helper()
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM testagram_local.notifications
			WHERE recipient_id=$1 AND post_id=$2 AND kind='mention'`, recipient, post).Scan(&count); err != nil {
			t.Fatalf("%s: count mention notifications: %v", label, err)
		}
		if count != want {
			t.Errorf("%s: mention notification count = %d, want %d", label, count, want)
		}
	}
	assertCount("public accepted follower", followerID, publicPostID, 1)
	assertCount("public blocked stranger", strangerID, publicPostID, 0)

	if err := createSocialMentionNotifications(ctx, tx, authorID, followersPostID, "followers", publicText); err != nil {
		t.Fatalf("create followers-only mention: %v", err)
	}
	assertCount("followers-only accepted follower", followerID, followersPostID, 1)
	assertCount("followers-only blocked stranger", strangerID, followersPostID, 0)

	if err := createSocialMentionNotifications(ctx, tx, authorID, privatePostID, "private", publicText); err != nil {
		t.Fatalf("suppress private mentions: %v", err)
	}
	assertCount("private post", followerID, privatePostID, 0)
	if err := createSocialMentionNotifications(ctx, tx, protectedID, protectedPostID, "public", publicText); err != nil {
		t.Fatalf("suppress protected public post mentions: %v", err)
	}
	assertCount("protected public post", followerID, protectedPostID, 0)
	if err := createSocialMentionNotifications(ctx, tx, authorID, blockedPostID, "public", "Hello @ci_mention_stranger_"+suffix); err != nil {
		t.Fatalf("suppress blocked mention: %v", err)
	}
	assertCount("blocked recipient", strangerID, blockedPostID, 0)
}
