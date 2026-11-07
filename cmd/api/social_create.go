package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type socialCreatePostRequest struct {
	Content    string `json:"content"`
	Visibility string `json:"visibility"`
	Sensitive  bool   `json:"sensitive"`
}

type socialCreatedPost struct {
	ID         string    `json:"id"`
	LegacyID   string    `json:"$id"`
	AuthorID   string    `json:"author_id"`
	UserID     string    `json:"user_id"`
	Content    string    `json:"content"`
	Body       string    `json:"body"`
	Visibility string    `json:"visibility"`
	Sensitive  bool      `json:"sensitive"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
}

var socialIdempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{16,128}$`)

func socialPostRequestHash(input socialCreatePostRequest) string {
	raw, _ := json.Marshal(input)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func socialIdempotencyKeyHash(key string) (string, error) {
	key = strings.TrimSpace(key)
	if !socialIdempotencyKeyPattern.MatchString(key) {
		return "", errors.New("Idempotency-Key must contain 16 to 128 safe characters")
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:]), nil
}

func (s *service) socialCreatePost(w http.ResponseWriter, r *http.Request) {
	userID, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	if !s.socialWriteAllowed(w, r, userID, requestID, "social-post-create", 10) {
		return
	}
	keyHash, err := socialIdempotencyKeyHash(r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("IDEMPOTENCY_KEY_REQUIRED", err.Error(), requestID))
		return
	}
	var input socialCreatePostRequest
	if err := decodeMediaJSON(w, r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_POST", "Post request must contain valid JSON", requestID))
		return
	}
	input.Content = strings.TrimSpace(input.Content)
	if input.Content == "" || len(input.Content) > 10000 {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_POST_CONTENT", "Post content must contain 1 to 10000 bytes", requestID))
		return
	}
	if input.Visibility == "" {
		input.Visibility = "public"
	}
	switch input.Visibility {
	case "public", "followers", "private":
	default:
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_POST_VISIBILITY", "Visibility must be public, followers, or private", requestID))
		return
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_STORE_UNAVAILABLE", "Post storage is unavailable", requestID))
		return
	}

	ctx := r.Context()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_CREATE_FAILED", "Post could not be saved", requestID))
		return
	}
	defer tx.Rollback(ctx)

	requestHash := socialPostRequestHash(input)
	var insertedKey string
	err = tx.QueryRow(ctx, `INSERT INTO testagram_local.post_idempotency(author_id,key_hash,request_hash)
		VALUES($1,$2,$3) ON CONFLICT(author_id,key_hash) DO NOTHING RETURNING key_hash`,
		userID, keyHash, requestHash).Scan(&insertedKey)
	if errors.Is(err, pgx.ErrNoRows) {
		var existingHash, existingPostID string
		if lookupErr := tx.QueryRow(ctx, `SELECT request_hash,COALESCE(post_id::text,'')
			FROM testagram_local.post_idempotency WHERE author_id=$1 AND key_hash=$2`,
			userID, keyHash).Scan(&existingHash, &existingPostID); lookupErr != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_IDEMPOTENCY_LOOKUP_FAILED", "Post retry state could not be verified", requestID))
			return
		}
		if existingHash != requestHash {
			writeJSON(w, http.StatusConflict, errorBody("IDEMPOTENCY_KEY_REUSED", "This Idempotency-Key was already used for a different post", requestID))
			return
		}
		if existingPostID == "" {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("IDEMPOTENT_POST_UNAVAILABLE", "The original post is no longer available; use a new key for a new post", requestID))
			return
		}
		var post socialCreatedPost
		if err := tx.QueryRow(ctx, `SELECT id::text,author_id::text,text,visibility,sensitive,created_at
			FROM testagram_local.posts WHERE id=$1 AND author_id=$2 AND deleted_at IS NULL`,
			existingPostID, userID).Scan(&post.ID, &post.AuthorID, &post.Content, &post.Visibility, &post.Sensitive, &post.CreatedAt); err != nil {
			writeJSON(w, http.StatusConflict, errorBody("IDEMPOTENT_POST_UNAVAILABLE", "The original post is no longer available", requestID))
			return
		}
		post.LegacyID, post.UserID, post.Body, post.Status = post.ID, post.AuthorID, post.Content, "published"
		if err := tx.Commit(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_READ_FAILED", "The original post could not be returned", requestID))
			return
		}
		w.Header().Set("Idempotency-Replayed", "true")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": post, "error": nil, "request_id": requestID})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_IDEMPOTENCY_RESERVE_FAILED", "Post retry protection is unavailable", requestID))
		return
	}

	var post socialCreatedPost
	err = tx.QueryRow(ctx, `INSERT INTO testagram_local.posts(author_id,text,visibility,sensitive)
		VALUES($1,$2,$3,$4)
		RETURNING id::text,author_id::text,text,visibility,sensitive,created_at`,
		userID, input.Content, input.Visibility, input.Sensitive).Scan(
		&post.ID, &post.AuthorID, &post.Content, &post.Visibility, &post.Sensitive, &post.CreatedAt)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_INSERT_FAILED", "Post could not be saved", requestID))
		return
	}
	if _, err := tx.Exec(ctx, `UPDATE testagram_local.post_idempotency SET post_id=$3
		WHERE author_id=$1 AND key_hash=$2`, userID, keyHash, post.ID); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_IDEMPOTENCY_COMMIT_FAILED", "Post retry protection could not be finalized", requestID))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_CREATE_FAILED", "Post could not be committed", requestID))
		return
	}
	post.LegacyID, post.UserID, post.Body, post.Status = post.ID, post.AuthorID, post.Content, "published"
	w.Header().Set("Location", "/api/social/posts/"+post.ID)
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "data": post, "error": nil, "request_id": requestID})
}
