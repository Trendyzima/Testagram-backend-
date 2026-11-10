package main

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// socialProfileConnections returns a bounded, privacy-filtered followers or
// following page. The owner must be discoverable and, for protected accounts,
// the viewer must have an accepted follow relationship.
func (s *service) socialProfileConnections(w http.ResponseWriter, r *http.Request) {
	viewerID, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	ownerID := strings.ToLower(strings.TrimSpace(r.PathValue("user_id")))
	if !socialUUIDPattern.MatchString(ownerID) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_PROFILE_ID", "A valid profile ID is required", requestID))
		return
	}
	kind := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("kind")))
	if kind != "followers" && kind != "following" {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_CONNECTION_KIND", "kind must be followers or following", requestID))
		return
	}
	limit := 30
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_LIMIT", "Limit must be between 1 and 100", requestID))
			return
		}
		limit = n
	}
	var cursor *socialCursor
	if raw := strings.TrimSpace(r.URL.Query().Get("cursor")); raw != "" {
		decoded, err := decodeSocialCursor(raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_CURSOR", "Connection cursor is invalid", requestID))
			return
		}
		cursor = &decoded
	}

	var relationshipColumn string
	if kind == "followers" {
		relationshipColumn = "f.follower_id"
	} else {
		relationshipColumn = "f.following_id"
	}
	query := `
SELECT a.user_id::text,a.username,a.display_name,a.avatar_key,a.bio,
       f.created_at,
       COALESCE((SELECT vf.status FROM testagram_local.follows vf
         WHERE vf.follower_id=$1 AND vf.following_id=a.user_id),'') AS viewer_follow_status,
       EXISTS(SELECT 1 FROM testagram_local.follows vf
         WHERE vf.follower_id=$1 AND vf.following_id=a.user_id AND vf.status='accepted') AS is_following
FROM testagram_local.follows f
JOIN testagram_local.accounts a ON a.user_id=` + relationshipColumn + `
WHERE ` + map[string]string{"followers":"f.following_id=$2","following":"f.follower_id=$2"}[kind] + `
  AND f.status='accepted'
  AND a.account_status='active'
  AND NOT EXISTS(SELECT 1 FROM testagram_local.blocks b
    WHERE (b.blocker_id=$1 AND b.blocked_id=a.user_id)
       OR (b.blocker_id=a.user_id AND b.blocked_id=$1))
  AND ($3::timestamptz IS NULL OR (f.created_at,a.user_id)<($3::timestamptz,$4::uuid))
ORDER BY f.created_at DESC,a.user_id DESC
LIMIT $5`

	// The profile's own discoverability and protected-account policy controls
	// whether its social graph can be enumerated by this viewer.
	var canEnumerate bool
	err := s.db.QueryRow(r.Context(), `
SELECT a.user_id=$1 OR EXISTS(
  SELECT 1 FROM testagram_local.follows f
  WHERE f.follower_id=$1 AND f.following_id=a.user_id AND f.status='accepted'
) OR (a.discoverable_by_username AND NOT a.protected_account)
FROM testagram_local.accounts a
WHERE a.user_id=$2 AND a.account_status='active'
  AND NOT EXISTS(SELECT 1 FROM testagram_local.blocks b
    WHERE (b.blocker_id=$1 AND b.blocked_id=a.user_id)
       OR (b.blocker_id=a.user_id AND b.blocked_id=$1))`,
		viewerID, ownerID).Scan(&canEnumerate)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !canEnumerate) {
		writeJSON(w, http.StatusNotFound, errorBody("PROFILE_NOT_FOUND", "The profile is unavailable", requestID))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("CONNECTIONS_UNAVAILABLE", "Profile connections could not be checked", requestID))
		return
	}
	var before any
	var beforeID any
	if cursor != nil {
		before, beforeID = cursor.CreatedAt, cursor.ID
	}
	rows, err := s.db.Query(r.Context(), query, viewerID, ownerID, before, beforeID, limit+1)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("CONNECTIONS_UNAVAILABLE", "Profile connections could not be loaded", requestID))
		return
	}
	defer rows.Close()
	type connection struct {
		ID string `json:"id"`
		Username string `json:"username"`
		DisplayName string `json:"display_name"`
		AvatarURL string `json:"avatar_url"`
		Bio string `json:"bio"`
		FollowStatus string `json:"follow_status"`
		IsFollowing bool `json:"is_following"`
		CreatedAt time.Time `json:"created_at"`
	}
	items := make([]connection, 0, limit)
	var next socialCursor
	hasMore := false
	for rows.Next() {
		var item connection
		var avatarKey string
		if err := rows.Scan(&item.ID,&item.Username,&item.DisplayName,&avatarKey,&item.Bio,&item.CreatedAt,&item.FollowStatus,&item.IsFollowing); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("CONNECTIONS_UNAVAILABLE", "Profile connections could not be decoded", requestID))
			return
		}
		if len(items) == limit {
			hasMore = true
			break
		}
		item.AvatarURL = publicMediaURL(avatarKey)
		items = append(items,item)
		next = socialCursor{CreatedAt:item.CreatedAt,ID:item.ID}
	}
	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("CONNECTIONS_UNAVAILABLE", "Profile connections could not be loaded", requestID))
		return
	}
	var nextCursor string
	if hasMore && len(items)>0 {
		nextCursor=encodeSocialCursor(next)
	}
	writeJSON(w,http.StatusOK,map[string]any{
		"ok":true,
		"data":map[string]any{"items":items,"next_cursor":nextCursor,"has_more":hasMore,"kind":kind},
		"error":nil,"request_id":requestID,
	})
}
