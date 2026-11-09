package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type createPostV1Request struct {
	Content    string `json:"content"`
	Visibility string `json:"visibility"`
	Sensitive  bool   `json:"sensitive"`
}

// createPostV1 is the native Appwrite-authenticated post creation route.
// Canonical account IDs are resolved by socialAuth; post rows and idempotency
// state are owned by Neon/PostgreSQL, never by a browser database client.
func (s *service) createPostV1(w http.ResponseWriter, r *http.Request) {
	userID, id, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	defer r.Body.Close()

	var input createPostV1Request
	if err := decodeSingleJSON(r.Body, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_JSON", "Request body must contain one valid JSON object", id))
		return
	}
	if code, message := validateCreatePostV1(&input, r.Header.Get("Idempotency-Key")); code != "" {
		status := http.StatusBadRequest
		if code == "IDEMPOTENCY_KEY_REUSED" { status = http.StatusConflict }
		writeJSON(w, status, errorBody(code, message, id))
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_STORE_UNAVAILABLE", "Post storage is temporarily unavailable", id))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var postID string
	var createdAt time.Time
	err = tx.QueryRow(ctx, `
		INSERT INTO testagram_local.posts(author_id,text,visibility,sensitive,idempotency_key)
		VALUES($1::uuid,$2,$3,$4,$5)
		ON CONFLICT (author_id,idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING
		RETURNING id::text,created_at`,
		userID, input.Content, input.Visibility, input.Sensitive, idempotencyKey,
	).Scan(&postID, &createdAt)

	replayed := false
	if errors.Is(err, pgx.ErrNoRows) {
		var previousContent, previousVisibility string
		var previousSensitive bool
		var deletedAt *time.Time
		err = tx.QueryRow(ctx, `
			SELECT id::text,text,visibility,sensitive,created_at,deleted_at
			FROM testagram_local.posts
			WHERE author_id=$1::uuid AND idempotency_key=$2
		`, userID, idempotencyKey).Scan(&postID, &previousContent, &previousVisibility, &previousSensitive, &createdAt, &deletedAt)
		if err == nil {
			if deletedAt != nil {
				writeJSON(w, http.StatusConflict, errorBody("POST_ALREADY_DELETED", "This idempotent post was previously deleted", id))
				return
			}
			if previousContent != input.Content || previousVisibility != input.Visibility || previousSensitive != input.Sensitive {
				writeJSON(w, http.StatusConflict, errorBody("IDEMPOTENCY_KEY_REUSED", "This idempotency key was already used for a different post", id))
				return
			}
			replayed = true
		}
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_CREATE_FAILED", "Post could not be saved", id))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_COMMIT_FAILED", "Post could not be committed", id))
		return
	}

	w.Header().Set("Cache-Control", "private, no-store")
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{
		"ok": true,
		"data": map[string]any{
			"$id": postID, "id": postID, "post_id": postID,
			"author_id": userID, "body": input.Content, "content": input.Content,
			"visibility": input.Visibility, "sensitive": input.Sensitive,
			"status": "published", "created_at": createdAt.UTC().Format(time.RFC3339Nano),
			"replayed": replayed,
		},
		"error": nil, "request_id": id,
	})
}



// validateCreatePostV1 normalizes and bounds user-controlled post data before
// opening a transaction. It is kept pure so the validation contract is unit-tested.
func validateCreatePostV1(input *createPostV1Request, rawKey string) (string, string) {
	input.Content = strings.TrimSpace(input.Content)
	if input.Content == "" || utf8.RuneCountInString(input.Content) > 5000 {
		return "INVALID_POST_CONTENT", "Post text must contain between 1 and 5000 characters"
	}
	input.Visibility = strings.ToLower(strings.TrimSpace(input.Visibility))
	if input.Visibility == "" {
		input.Visibility = "public"
	}
	if input.Visibility != "public" && input.Visibility != "followers" && input.Visibility != "private" {
		return "INVALID_POST_VISIBILITY", "Post visibility must be public, followers or private"
	}
	key := strings.TrimSpace(rawKey)
	if utf8.RuneCountInString(key) < 8 || utf8.RuneCountInString(key) > 128 || strings.ContainsAny(key, "\r\n") {
		return "IDEMPOTENCY_KEY_REQUIRED", "A valid Idempotency-Key header is required"
	}
	return "", ""
}
