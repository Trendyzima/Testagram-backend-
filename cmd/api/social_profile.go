package main

import (
	"encoding/json"
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
// verified native Testagram session. Clients must use the canonical profile ID.
func (s *service) socialMyProfileRead(w http.ResponseWriter, r *http.Request) {
	viewerID, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	s.socialProfileReadFor(w, r, viewerID, viewerID, requestID)
}

func (s *service) socialProfileReadFor(w http.ResponseWriter, r *http.Request, viewerID, targetID, requestID string) {
	// Keep response fields typed and never expose a storage key as a public URL.
	var id, username, displayName, bio, website, avatarKey, avatarProvider, coverKey, location, followStatus string
	var socialLinksJSON []byte
	var protected, following, followedBy, canViewPosts bool
	var followers, followingCount int64
	err := s.db.QueryRow(r.Context(), `
SELECT a.user_id::text,a.username,a.display_name,a.bio,a.website,a.avatar_key,a.avatar_storage_provider,a.cover_key,a.location,a.social_links,
       a.protected_account,a.follower_count,a.following_count,
       COALESCE((SELECT f.status FROM testagram_local.follows f WHERE f.follower_id=$1 AND f.following_id=a.user_id),''),
       EXISTS(SELECT 1 FROM testagram_local.follows f WHERE f.follower_id=$1 AND f.following_id=a.user_id AND f.status='accepted'),
       EXISTS(SELECT 1 FROM testagram_local.follows f WHERE f.follower_id=a.user_id AND f.following_id=$1 AND f.status='accepted'),
       (a.user_id=$1 OR NOT a.protected_account OR EXISTS(SELECT 1 FROM testagram_local.follows f WHERE f.follower_id=$1 AND f.following_id=a.user_id AND f.status='accepted'))
FROM testagram_local.accounts a
WHERE a.user_id=$2 AND a.account_status='active'
  AND (a.user_id=$1 OR a.discoverable_by_username OR EXISTS(SELECT 1 FROM testagram_local.follows f WHERE f.follower_id=$1 AND f.following_id=a.user_id AND f.status='accepted'))
  AND NOT EXISTS(SELECT 1 FROM testagram_local.blocks b WHERE (b.blocker_id=$1 AND b.blocked_id=a.user_id) OR (b.blocker_id=a.user_id AND b.blocked_id=$1))`,
		viewerID, targetID).Scan(&id, &username, &displayName, &bio, &website, &avatarKey, &avatarProvider,
		&coverKey, &location, &socialLinksJSON, &protected, &followers, &followingCount, &followStatus, &following, &followedBy, &canViewPosts)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, errorBody("PROFILE_NOT_FOUND", "The profile is unavailable", requestID))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("PROFILE_UNAVAILABLE", "The profile could not be loaded", requestID))
		return
	}
	avatarURL := publicMediaURL(avatarKey)
	switch avatarProvider {
	case "instabox-vps":
		avatarURL = profileAvatarURL(id)
	case "", "legacy-media":
		avatarProvider = "legacy-media"
	default:
		// Unknown and retired storage providers never produce a public URL.
		avatarURL = ""
		avatarProvider = "legacy-media"
	}
	socialLinks := map[string]string{}
	_ = json.Unmarshal(socialLinksJSON, &socialLinks)
	profile := map[string]any{
		"id": id, "user_id": id, "username": username, "display_name": displayName,
		"bio": bio, "website": website, "location": location, "social_links": socialLinks,
		"avatar_key": avatarKey, "avatar_url": avatarURL, "avatar_storage_provider": avatarProvider,
		"cover_key": coverKey, "cover_url": publicMediaURL(coverKey), "protected_account": protected,
		"followers_count": followers, "following_count": followingCount,
		"follow_status": followStatus, "is_following": following,
		"is_followed_by": followedBy, "can_view_posts": canViewPosts, "is_self": id == viewerID,
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "data": profile, "error": nil, "request_id": requestID,
	})
}


type socialProfileResolveRequest struct {
	Username string `json:"username"`
}

// validSocialUsername accepts only the public Testagram handle alphabet.
// Resolution remains viewer-scoped: private, undiscoverable and blocked
// accounts are not revealed by this lookup.
func validSocialUsername(value string) bool {
	if len(value) < 2 || len(value) > 30 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') &&
			!(r >= '0' && r <= '9') && r != '_' && r != '.' {
			return false
		}
	}
	return true
}

func (s *service) socialProfileResolveByUsername(w http.ResponseWriter, r *http.Request) {
	viewerID, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("PROFILE_UNAVAILABLE", "The profile could not be resolved", requestID))
		return
	}
	var input socialProfileResolveRequest
	if err := decodeMediaJSON(w, r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_PROFILE_USERNAME", "A valid profile username is required", requestID))
		return
	}
	input.Username = strings.TrimPrefix(strings.TrimSpace(input.Username), "@")
	if !validSocialUsername(input.Username) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_PROFILE_USERNAME", "A valid profile username is required", requestID))
		return
	}
	var targetID string
	err := s.db.QueryRow(r.Context(), `
SELECT a.user_id::text
FROM testagram_local.accounts a
WHERE lower(a.username)=lower($1)
  AND a.account_status='active'
  AND (a.user_id=$2 OR a.discoverable_by_username OR EXISTS(
    SELECT 1 FROM testagram_local.follows f
    WHERE f.follower_id=$2 AND f.following_id=a.user_id AND f.status='accepted'
  ))
  AND NOT EXISTS(
    SELECT 1 FROM testagram_local.blocks b
    WHERE (b.blocker_id=$2 AND b.blocked_id=a.user_id)
       OR (b.blocker_id=a.user_id AND b.blocked_id=$2)
  )`, input.Username, viewerID).Scan(&targetID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, errorBody("PROFILE_NOT_FOUND", "The profile is unavailable", requestID))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("PROFILE_UNAVAILABLE", "The profile could not be resolved", requestID))
		return
	}
	s.socialProfileReadFor(w, r, viewerID, targetID, requestID)
}
