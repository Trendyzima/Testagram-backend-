package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestSocialInteractionSummaryPostgresIntegration(t *testing.T) {
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

	suffix := fmt.Sprintf("%09d", time.Now().UnixNano()%1_000_000_000)
	var viewerID, authorID, postID string
	for _, fixture := range []struct {
		username string
		target   *string
	}{
		{"ci_summary_viewer_" + suffix, &viewerID},
		{"ci_summary_author_" + suffix, &authorID},
	} {
		if err := tx.QueryRow(ctx, "INSERT INTO testagram_local.accounts(user_id,username) VALUES(gen_random_uuid(),$1) RETURNING user_id::text", fixture.username).Scan(fixture.target); err != nil {
			t.Fatalf("insert account %s: %v", fixture.username, err)
		}
	}
	if err := tx.QueryRow(ctx, "INSERT INTO testagram_local.posts(author_id,text,visibility) VALUES($1,'summary integration post','public') RETURNING id::text", authorID).Scan(&postID); err != nil {
		t.Fatalf("insert post: %v", err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO testagram_local.post_reactions(post_id,user_id,emoji) VALUES($1,$2,'❤️'),($1,$3,'😂')", postID, viewerID, authorID); err != nil {
		t.Fatalf("insert reactions: %v", err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO testagram_local.replies(post_id,user_id,content) VALUES($1,$2,'summary reply')", postID, viewerID); err != nil {
		t.Fatalf("insert reply: %v", err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO testagram_local.reposts(post_id,user_id) VALUES($1,$2)", postID, viewerID); err != nil {
		t.Fatalf("insert repost: %v", err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO testagram_local.bookmarks(post_id,user_id) VALUES($1,$2)", postID, viewerID); err != nil {
		t.Fatalf("insert bookmark: %v", err)
	}
	if _, err := socialCanViewPostReadWith(ctx, tx, viewerID, postID); err != nil {
		t.Fatalf("viewer should be able to read public post: %v", err)
	}

	var likes, replies, reposts, bookmarks int64
	var emoji string
	var isReposted, isBookmarked bool
	var reactionsJSON []byte
	if err := tx.QueryRow(ctx, socialInteractionSummarySQL, postID, viewerID).Scan(
		&likes, &replies, &reposts, &bookmarks, &emoji, &isReposted, &isBookmarked, &reactionsJSON,
	); err != nil {
		t.Fatalf("execute production interaction summary query: %v", err)
	}
	var reactions map[string]int64
	if err := json.Unmarshal(reactionsJSON, &reactions); err != nil {
		t.Fatalf("decode reaction breakdown: %v", err)
	}
	if likes != 2 || replies != 1 || reposts != 1 || bookmarks != 1 {
		t.Fatalf("counts = likes:%d replies:%d reposts:%d bookmarks:%d; want 2/1/1/1", likes, replies, reposts, bookmarks)
	}
	if emoji != "❤️" || !isReposted || !isBookmarked {
		t.Fatalf("viewer state = emoji:%q reposted:%v bookmarked:%v; want heart/true/true", emoji, isReposted, isBookmarked)
	}
	if reactions["❤️"] != 1 || reactions["😂"] != 1 || len(reactions) != 2 {
		t.Fatalf("reaction breakdown = %#v; want heart:1 and laugh:1", reactions)
	}
}
