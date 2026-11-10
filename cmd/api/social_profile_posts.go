package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

)

// socialProfilePosts returns the authenticated viewer's visible page of posts
// for a profile. Protected-account and block rules are enforced in SQL before
// any post or media identifiers are returned.
func (s *service) socialProfilePosts(w http.ResponseWriter, r *http.Request) {
	viewerID, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	ownerID := strings.ToLower(strings.TrimSpace(r.PathValue("user_id")))
	if !socialUUIDPattern.MatchString(ownerID) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_PROFILE_ID", "A valid profile ID is required", requestID))
		return
	}
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 50 {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_LIMIT", "Profile post limit must be between 1 and 50", requestID))
			return
		}
		limit = n
	}
	cursor, err := decodeHomeFeedCursor(r.URL.Query().Get("before"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_CURSOR", "Profile post cursor is invalid", requestID))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	query := `
SELECT p.id::text,p.author_id::text,p.text,p.visibility,p.media_ids,
       p.language,p.created_at,p.likes_count,p.replies_count,p.reposts_count,
       a.username,a.display_name,a.avatar_key,a.protected_account
FROM testagram_local.posts p
JOIN testagram_local.accounts a ON a.user_id=p.author_id
WHERE p.author_id=$2
  AND p.deleted_at IS NULL
  AND a.account_status='active'
  AND (
    p.author_id=$1
    OR (p.visibility='public' AND NOT a.protected_account)
    OR (p.visibility IN ('public','followers') AND EXISTS(
      SELECT 1 FROM testagram_local.follows f
      WHERE f.follower_id=$1 AND f.following_id=a.user_id AND f.status='accepted'
    ))
  )
  AND NOT EXISTS(SELECT 1 FROM testagram_local.blocks b
    WHERE (b.blocker_id=$1 AND b.blocked_id=a.user_id)
       OR (b.blocker_id=a.user_id AND b.blocked_id=$1))
  AND ($3::timestamptz IS NULL OR (p.created_at,p.id)<($3::timestamptz,$4::uuid))
ORDER BY p.created_at DESC,p.id DESC
LIMIT $5`
	var before any
	var beforeID any
	if cursor != nil {
		before, beforeID = cursor.CreatedAt, cursor.ID
	}
	rows, err := s.db.Query(ctx, query, viewerID, ownerID, before, beforeID, limit+1)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("PROFILE_POSTS_UNAVAILABLE", "Profile posts could not be loaded", requestID))
		return
	}
	defer rows.Close()
	posts := make([]homeFeedPost, 0, limit)
	var next homeFeedCursor
	hasMore := false
	for rows.Next() {
		var post homeFeedPost
		var mediaRaw []byte
		var username, displayName, avatarKey string
		var protected bool
		if err := rows.Scan(&post.ID,&post.UserID,&post.Content,&post.Visibility,&mediaRaw,
			&post.Language,&post.CreatedAt,&post.LikesCount,&post.RepliesCount,&post.RepostsCount,
			&username,&displayName,&avatarKey,&protected); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("PROFILE_POSTS_UNAVAILABLE", "Profile posts could not be decoded", requestID))
			return
		}
		if len(posts)==limit {
			hasMore=true
			break
		}
		post.MediaIDs=[]string{}
		_ = json.Unmarshal(mediaRaw,&post.MediaIDs)
		post.Author=map[string]any{"id":post.UserID,"username":username,"display_name":displayName,
			"avatar_key":avatarKey,"avatar_url":publicMediaURL(avatarKey),"protected_account":protected}
		post.Origin="local"
		posts=append(posts,post)
		next=homeFeedCursor{CreatedAt:post.CreatedAt,ID:post.ID}
	}
	if err:=rows.Err();err!=nil {
		writeJSON(w,http.StatusServiceUnavailable,errorBody("PROFILE_POSTS_UNAVAILABLE","Profile posts could not be loaded",requestID))
		return
	}
	rows.Close()
	if err:=s.attachHomeFeedMediaURLs(ctx,viewerID,posts);err!=nil {
		writeJSON(w,http.StatusServiceUnavailable,errorBody("PROFILE_POST_MEDIA_UNAVAILABLE","Profile post media could not be authorized",requestID))
		return
	}
	var nextCursor string
	if hasMore && len(posts)>0 {
		nextCursor=encodeHomeFeedCursor(next)
	}
	writeJSON(w,http.StatusOK,map[string]any{
		"ok":true,"data":map[string]any{"posts":posts,"has_more":hasMore,"next_cursor":nextCursor},
		"error":nil,"request_id":requestID,
	})
}
