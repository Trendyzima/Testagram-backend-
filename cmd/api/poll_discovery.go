package main

import (
	"net/http"
	"strconv"
	"time"
)

func (s *service) pollsList(w http.ResponseWriter, r *http.Request) {
	id := requestID(r)
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", id))
		return
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POLL_SERVICE_UNAVAILABLE", "Poll service is unavailable", id))
		return
	}

	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_POLL_LIMIT", "Limit must be between 1 and 100", id))
			return
		}
		limit = n
	}
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > 10000 {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_POLL_OFFSET", "Offset must be between 0 and 10000", id))
			return
		}
		offset = n
	}

	rows, err := s.db.Query(r.Context(), `
		SELECT p.id::text, p.post_id::text, p.question, COALESCE(p.description, ''),
		       p.ends_at, p.total_votes, p.created_at, COALESCE(c.name, '')
		FROM testagram_local.polls p
		JOIN testagram_local.posts post ON post.id = p.post_id
		LEFT JOIN testagram_local.communities c ON c.id = post.community_id
		WHERE p.status = 'open'
		  AND (p.ends_at IS NULL OR p.ends_at > now())
		  AND post.deleted_at IS NULL
		  AND post.visibility = 'public'
		  AND (c.id IS NULL OR c.visibility = 'public')
		ORDER BY p.total_votes DESC, p.created_at DESC, p.id DESC
		LIMIT $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POLL_LIST_FAILED", "Polls could not be loaded", id))
		return
	}
	defer rows.Close()

	items := make([]map[string]any, 0)
	for rows.Next() {
		var pollID, postID, question, description, communityName string
		var endsAt *time.Time
		var totalVotes int64
		var createdAt time.Time
		if err := rows.Scan(&pollID, &postID, &question, &description, &endsAt, &totalVotes, &createdAt, &communityName); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("POLL_LIST_FAILED", "Poll records could not be read", id))
			return
		}
		items = append(items, map[string]any{
			"id": pollID,
			"poll_id": pollID,
			"post_id": postID,
			"question": question,
			"description": description,
			"ends_at": endsAt,
			"status": "open",
			"total_votes": totalVotes,
			"created_at": createdAt,
			"community_display_name": communityName,
		})
	}
	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POLL_LIST_FAILED", "Poll records could not be read", id))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"data": map[string]any{"items": items, "next_cursor": nil},
		"error": nil,
		"request_id": id,
	})
}
