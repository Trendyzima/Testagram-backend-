package main

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// This test verifies the exact aggregate semantics behind trending discovery.
// It is skipped outside the isolated PostgreSQL CI database.
func TestTrendingTopicMomentumPostgresIntegration(t *testing.T) {
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
		t.Fatalf("begin trend fixture transaction: %v", err)
	}
	defer tx.Rollback(context.Background())

	suffix := fmt.Sprintf("%09d", time.Now().UnixNano()%1_000_000_000)
	var publicAuthorA, publicAuthorB, protectedAuthor string
	for _, fixture := range []struct {
		username string
		protected bool
		target *string
	}{
		{"trend_public_a_" + suffix, false, &publicAuthorA},
		{"trend_public_b_" + suffix, false, &publicAuthorB},
		{"trend_protected_" + suffix, true, &protectedAuthor},
	} {
		if err := tx.QueryRow(ctx, `
INSERT INTO testagram_local.accounts(user_id,username,protected_account)
VALUES(gen_random_uuid(),$1,$2) RETURNING user_id::text`,
			fixture.username, fixture.protected).Scan(fixture.target); err != nil {
			t.Fatalf("insert trend fixture account: %v", err)
		}
	}

	tag := "trendmomentum" + suffix
	word := "wordmomentum" + suffix
	posts := []struct {
		author string
		visibility string
		content string
		age string
	}{
		{publicAuthorA, "public", "#" + tag + " " + word, "1 hour"},
		{publicAuthorB, "public", "#" + tag + " " + word, "30 hours"},
		{publicAuthorA, "private", "#private" + suffix + " privateword" + suffix, "1 hour"},
		{protectedAuthor, "public", "#protected" + suffix + " protectedword" + suffix, "1 hour"},
		{publicAuthorA, "followers", "#followers" + suffix + " followersword" + suffix, "1 hour"},
	}
	for _, post := range posts {
		if _, err := tx.Exec(ctx, `
INSERT INTO testagram_local.posts(author_id,text,visibility,created_at)
VALUES($1,$2,$3,now()-$4::interval)`,
			post.author, post.content, post.visibility, post.age); err != nil {
			t.Fatalf("insert trend fixture post: %v", err)
		}
	}

	var hashtagPosts, hashtagRecent, hashtagPrevious int64
	err = tx.QueryRow(ctx, `
SELECT count(DISTINCT p.id)::bigint,
       (count(DISTINCT p.id) FILTER (WHERE p.created_at >= now()-interval '24 hours'))::bigint,
       (count(DISTINCT p.id) FILTER (WHERE p.created_at >= now()-interval '48 hours'
          AND p.created_at < now()-interval '24 hours'))::bigint
FROM testagram_local.posts p
JOIN testagram_local.accounts a ON a.user_id=p.author_id
CROSS JOIN LATERAL regexp_matches(p.text,'#([[:alnum:]_]{1,64})','g') AS extracted(parts)
WHERE p.deleted_at IS NULL AND p.visibility='public'
  AND a.account_status='active' AND NOT a.protected_account
  AND p.created_at >= now()-interval '7 days'
  AND lower(parts[1])=$1`, tag).Scan(&hashtagPosts, &hashtagRecent, &hashtagPrevious)
	if err != nil {
		t.Fatalf("query public hashtag momentum: %v", err)
	}
	if hashtagPosts != 2 || hashtagRecent != 1 || hashtagPrevious != 1 {
		t.Fatalf("hashtag volume/recent/previous = %d/%d/%d, want 2/1/1",
			hashtagPosts, hashtagRecent, hashtagPrevious)
	}

	var wordPosts, wordRecent, wordPrevious int64
	err = tx.QueryRow(ctx, `
SELECT count(DISTINCT p.id)::bigint,
       (count(DISTINCT p.id) FILTER (WHERE p.created_at >= now()-interval '24 hours'))::bigint,
       (count(DISTINCT p.id) FILTER (WHERE p.created_at >= now()-interval '48 hours'
          AND p.created_at < now()-interval '24 hours'))::bigint
FROM testagram_local.posts p
JOIN testagram_local.accounts a ON a.user_id=p.author_id
CROSS JOIN LATERAL regexp_matches(lower(p.text),'([[:alpha:]][[:alnum:]_]{2,})','g') AS extracted(parts)
WHERE p.deleted_at IS NULL AND p.visibility='public'
  AND a.account_status='active' AND NOT a.protected_account
  AND p.created_at >= now()-interval '7 days'
  AND char_length(parts[1]) BETWEEN 4 AND 48
  AND lower(parts[1]) <> ALL($1::text[])
  AND lower(parts[1]) !~ '^[0-9_]+$'
  AND lower(parts[1])=$2
HAVING count(DISTINCT p.id) >= 2`, trendingWordStopwords, word).
		Scan(&wordPosts, &wordRecent, &wordPrevious)
	if err != nil {
		t.Fatalf("query public trending word momentum: %v", err)
	}
	if wordPosts != 2 || wordRecent != 1 || wordPrevious != 1 {
		t.Fatalf("word volume/recent/previous = %d/%d/%d, want 2/1/1",
			wordPosts, wordRecent, wordPrevious)
	}
}
