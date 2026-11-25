package main

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func parseSocialSuggestionLimit(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 8, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 || limit > 20 {
		return 0, strconv.ErrSyntax
	}
	return limit, nil
}

func socialSuggestionReason(mutualFollowers, recentPosts int64) string {
	if mutualFollowers > 0 {
		return "followed_by_people_you_follow"
	}
	if recentPosts > 0 {
		return "active_in_testagram"
	}
	return "popular_in_testagram"
}

// socialAccountSuggestions returns discoverable accounts ranked from the
// viewer's accepted follow graph and recent public activity. It deliberately
// excludes protected accounts, existing/pending follows, blocks and mutes.
func (s *service) socialAccountSuggestions(w http.ResponseWriter, r *http.Request) {
	viewerID, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	if !s.socialWriteAllowed(w, r, viewerID, requestID, "social-suggestions", 60) {
		return
	}
	limit, err := parseSocialSuggestionLimit(r.URL.Query().Get("limit"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_LIMIT", "Suggestion limit must be between 1 and 20", requestID))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := s.db.Query(ctx, `
SELECT a.user_id::text,a.username,a.display_name,a.avatar_key,a.bio,
       mutual.shared_followers,
       activity.recent_posts,
       followers.follower_count,
       CASE WHEN EXISTS(
         SELECT 1 FROM testagram_local.follows own
         WHERE own.follower_id=$1::uuid AND own.following_id=a.user_id AND own.status='accepted'
       ) THEN TRUE ELSE FALSE END AS is_following
FROM testagram_local.accounts a
CROSS JOIN LATERAL (
  SELECT count(*)::bigint AS shared_followers
  FROM testagram_local.follows mine
  JOIN testagram_local.follows theirs
    ON theirs.follower_id=mine.following_id
   AND theirs.following_id=a.user_id
   AND theirs.status='accepted'
  WHERE mine.follower_id=$1::uuid AND mine.status='accepted'
) mutual
CROSS JOIN LATERAL (
  SELECT count(*)::bigint AS recent_posts
  FROM testagram_local.posts p
  WHERE p.author_id=a.user_id AND p.deleted_at IS NULL AND p.visibility='public'
    AND p.created_at >= now()-interval '7 days'
) activity
CROSS JOIN LATERAL (
  SELECT count(*)::bigint AS follower_count
  FROM testagram_local.follows f
  WHERE f.following_id=a.user_id AND f.status='accepted'
) followers
WHERE a.account_status='active'
  AND a.discoverable_by_username=TRUE
  AND a.protected_account=FALSE
  AND a.user_id<>$1::uuid
  AND NOT EXISTS (
    SELECT 1 FROM testagram_local.follows f
    WHERE f.follower_id=$1::uuid AND f.following_id=a.user_id
  )
  AND NOT EXISTS (
    SELECT 1 FROM testagram_local.blocks b
    WHERE (b.blocker_id=$1::uuid AND b.blocked_id=a.user_id)
       OR (b.blocker_id=a.user_id AND b.blocked_id=$1::uuid)
  )
  AND NOT EXISTS (
    SELECT 1 FROM testagram_local.mutes m
    WHERE m.muter_id=$1::uuid AND m.muted_id=a.user_id
  )
ORDER BY mutual.shared_followers DESC,activity.recent_posts DESC,
         followers.follower_count DESC,a.created_at DESC,a.user_id
LIMIT $2`, viewerID, limit)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("SUGGESTIONS_UNAVAILABLE", "Account recommendations could not be loaded", requestID))
		return
	}
	defer rows.Close()

	items := make([]map[string]any, 0, limit)
	for rows.Next() {
		var id, username, displayName, avatarKey, bio string
		var mutualFollowers, recentPosts, followerCount int64
		var following bool
		if err := rows.Scan(&id, &username, &displayName, &avatarKey, &bio,
			&mutualFollowers, &recentPosts, &followerCount, &following); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("SUGGESTIONS_UNAVAILABLE", "Account recommendations could not be decoded", requestID))
			return
		}
		items = append(items, map[string]any{
			"id": id, "username": username, "display_name": displayName,
			"avatar_url": publicMediaURL(avatarKey), "bio": bio,
			"mutual_followers_count": mutualFollowers, "recent_posts_count": recentPosts,
			"followers_count": followerCount, "is_following": following,
			"reason": socialSuggestionReason(mutualFollowers, recentPosts),
		})
	}
	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("SUGGESTIONS_UNAVAILABLE", "Account recommendations could not be loaded", requestID))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "data": map[string]any{"items": items},
		"error": nil, "request_id": requestID,
	})
}
