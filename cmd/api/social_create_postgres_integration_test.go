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

func TestSocialPostIdempotencyPostgresIntegration(t *testing.T) {
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
	var userID string
	if err := tx.QueryRow(ctx, `INSERT INTO testagram_local.accounts(user_id,username)
		VALUES(gen_random_uuid(),$1) RETURNING user_id::text`, "ci_post_idem_"+suffix).Scan(&userID); err != nil {
		t.Fatalf("insert account fixture: %v", err)
	}
	input := socialCreatePostRequest{Content: "idempotent post " + suffix, Visibility: "public", Sensitive: true}
	keyHash, err := socialIdempotencyKeyHash("ci-post-create-" + suffix)
	if err != nil {
		t.Fatalf("hash fixture idempotency key: %v", err)
	}
	requestHash := socialPostRequestHash(input)
	var reserved string
	if err := tx.QueryRow(ctx, `INSERT INTO testagram_local.post_idempotency(author_id,key_hash,request_hash)
		VALUES($1,$2,$3) ON CONFLICT(author_id,key_hash) DO NOTHING RETURNING key_hash`,
		userID, keyHash, requestHash).Scan(&reserved); err != nil {
		t.Fatalf("reserve idempotency key: %v", err)
	}
	var postID string
	if err := tx.QueryRow(ctx, `INSERT INTO testagram_local.posts(author_id,text,visibility,sensitive)
		VALUES($1,$2,$3,$4) RETURNING id::text`, userID, input.Content, input.Visibility, input.Sensitive).Scan(&postID); err != nil {
		t.Fatalf("insert post fixture: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE testagram_local.post_idempotency SET post_id=$3
		WHERE author_id=$1 AND key_hash=$2`, userID, keyHash, postID); err != nil {
		t.Fatalf("finalize idempotency fixture: %v", err)
	}

	var duplicate string
	err = tx.QueryRow(ctx, `INSERT INTO testagram_local.post_idempotency(author_id,key_hash,request_hash)
		VALUES($1,$2,$3) ON CONFLICT(author_id,key_hash) DO NOTHING RETURNING key_hash`,
		userID, keyHash, requestHash).Scan(&duplicate)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("duplicate key reservation error = %v, want pgx.ErrNoRows", err)
	}
	var storedHash, storedPostID string
	if err := tx.QueryRow(ctx, `SELECT request_hash,post_id::text FROM testagram_local.post_idempotency
		WHERE author_id=$1 AND key_hash=$2`, userID, keyHash).Scan(&storedHash, &storedPostID); err != nil {
		t.Fatalf("read idempotency record: %v", err)
	}
	if storedHash != requestHash || storedPostID != postID {
		t.Fatalf("stored idempotency record = (%q,%q), want (%q,%q)", storedHash, storedPostID, requestHash, postID)
	}
	if storedHash == socialPostRequestHash(socialCreatePostRequest{Content: input.Content, Visibility: "private", Sensitive: true}) {
		t.Fatal("same key with a different visibility must be detected as a request conflict")
	}
	var sensitive bool
	if err := tx.QueryRow(ctx, "SELECT sensitive FROM testagram_local.posts WHERE id=$1", postID).Scan(&sensitive); err != nil || !sensitive {
		t.Fatalf("sensitive post field = %v, err=%v; want true", sensitive, err)
	}
}
