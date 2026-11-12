package main

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

// socialProfileRead returns an active, discoverable profile that the
// authenticated viewer is permitted to resolve.
func (s *service) socialProfileRead(w http.ResponseWriter, r *http.Request) {
	viewerID, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	targetID := strings.ToLower(strings.TrimSpace(r.PathValue("user_id")))
	if !socialUUIDPattern.MatchString(targetID) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_PROFILE_ID", "A valid profile ID is required", requestID))
		return
	}
	s.socialProfileReadFor(w, r, viewerID, targetID, requestID)
}

// socialMyProfileRead resolves the caller's canonical Testagram UUID from the
// verified Appwrite session. Clients must not use the Appwrite user ID as a
// profile ID: the two identifiers are intentionally distinct.
func (s *service) socialMyProfileRead(w http.ResponseWriter, r *http.Request) {
	viewerID, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	s.socialProfileReadFor(w, r, viewerID, viewerID, requestID)
}

func (s *service) socialProfileReadFor(w http.ResponseWriter, r *http.Request, viewerID, targetID, requestID string) {
	// Keep response fields typed and never expose a storage key as a public URL.
	var id, username, displayName, bio, website, avatarKey, followStatus string
	var protected, following, followedBy, canViewPosts bool
	var followers, followingCount int64
	err := s.db.QueryRow(r.Context(), `
SELECT a.user_id::text,a.username,a.display_name,a.bio,a.website,a.avatar_key,
       a.protected_account,a.follower_count,a.following_count,
       COALESCE((SELECT f.status FROM testagram_local.follows f WHERE f.follower_id=$1 AND f.following_id=a.user_id),''),
       EXISTS(SELECT 1 FROM testagram_local.follows f WHERE f.follower_id=$1 AND f.following_id=a.user_id AND f.status='accepted'),
       EXISTS(SELECT 1 FROM testagram_local.follows f WHERE f.follower_id=a.user_id AND f.following_id=$1 AND f.status='accepted'),
       (a.user_id=$1 OR NOT a.protected_account OR EXISTS(SELECT 1 FROM testagram_local.follows f WHERE f.follower_id=$1 AND f.following_id=a.user_id AND f.status='accepted'))
FROM testagram_local.accounts a
WHERE a.user_id=$2 AND a.account_status='active'
  AND (a.user_id=$1 OR a.discoverable_by_username OR EXISTS(SELECT 1 FROM testagram_local.follows f WHERE f.follower_id=$1 AND f.following_id=a.user_id AND f.status='accepted'))
  AND NOT EXISTS(SELECT 1 FROM testagram_local.blocks b WHERE (b.blocker_id=$1 AND b.blocked_id=a.user_id) OR (b.blocker_id=a.user_id AND b.blocked_id=$1))`,
		viewerID, targetID).Scan(&id, &username, &displayName, &bio, &website, &avatarKey,
		&protected, &followers, &followingCount, &followStatus, &following, &followedBy, &canViewPosts)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, errorBody("PROFILE_NOT_FOUND", "The profile is unavailable", requestID))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("PROFILE_UNAVAILABLE", "The profile could not be loaded", requestID))
		return
	}
	profile := map[string]any{
		"id": id, "user_id": id, "username": username, "display_name": displayName,
		"bio": bio, "website": website, "avatar_key": avatarKey,
		"avatar_url": publicMediaURL(avatarKey), "protected_account": protected,
		"followers_count": followers, "following_count": followingCount,
		"follow_status": followStatus, "is_following": following,
		"is_followed_by": followedBy, "can_view_posts": canViewPosts, "is_self": id == viewerID,
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "data": profile, "error": nil, "request_id": requestID,
	})
}
