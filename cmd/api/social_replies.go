package main

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *service) socialPostRepliesList(w http.ResponseWriter, r *http.Request) {
	userID, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	postID := strings.ToLower(strings.TrimSpace(r.PathValue("post_id")))
	if !socialUUIDPattern.MatchString(postID) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_POST_ID", "A valid post ID is required", requestID))
		return
	}
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_LIMIT", "Limit must be between 1 and 100", requestID))
			return
		}
		if parsed < limit {
			limit = parsed
		}
		if parsed > 100 {
			limit = 100
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("REPLIES_UNAVAILABLE", "Replies could not be loaded", requestID))
		return
	}
	defer tx.Rollback(ctx)

	if _, err := socialCanViewPostReadWith(ctx, tx, userID, postID); errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, errorBody("POST_NOT_FOUND", "The post is unavailable", requestID))
		return
	} else if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_LOOKUP_FAILED", "Post visibility could not be checked", requestID))
		return
	}

	rows, err := tx.Query(ctx, `SELECT r.id::text,r.post_id::text,r.user_id::text,
		r.parent_reply_id::text,r.content,r.created_at,
		a.username,a.display_name,a.avatar_key
		FROM testagram_local.replies r
		JOIN testagram_local.accounts a ON a.user_id=r.user_id
		WHERE r.post_id=$1 AND r.deleted_at IS NULL
		ORDER BY r.created_at DESC,r.id DESC
		LIMIT $2`, postID, limit)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("REPLIES_UNAVAILABLE", "Replies could not be loaded", requestID))
		return
	}
	items := make([]map[string]any, 0, limit)
	for rows.Next() {
		var id, replyPostID, authorID, body, username, displayName, avatarKey string
		var parentID *string
		var createdAt time.Time
		if err := rows.Scan(&id, &replyPostID, &authorID, &parentID, &body, &createdAt, &username, &displayName, &avatarKey); err != nil {
			rows.Close()
			writeJSON(w, http.StatusServiceUnavailable, errorBody("REPLIES_UNAVAILABLE", "Replies could not be decoded", requestID))
			return
		}
		items = append(items, map[string]any{
			"id": id, "post_id": replyPostID, "user_id": authorID,
			"parent_reply_id": parentID, "content": body, "created_at": createdAt,
			"user_profiles": map[string]any{
				"id": authorID, "username": username, "display_name": displayName, "avatar_url": avatarKey,
			},
		})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeJSON(w, http.StatusServiceUnavailable, errorBody("REPLIES_UNAVAILABLE", "Replies could not be loaded", requestID))
		return
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("REPLIES_UNAVAILABLE", "Replies could not be confirmed", requestID))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "data": map[string]any{"items": items, "has_more": len(items) == limit},
		"error": nil, "request_id": requestID,
	})
}
