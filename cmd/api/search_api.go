package main

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (s *service) searchAPI(w http.ResponseWriter, r *http.Request) {
	userID, requestID, ok := s.socialOptionalReadAuth(w, r)
	if !ok {
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(query) < 2 || len(query) > 120 {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_SEARCH_QUERY", "Search text must contain 2 to 120 characters", requestID))
		return
	}
	kind := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("kind")))
	if kind == "" {
		kind = "all"
	}
	if kind != "all" && kind != "posts" && kind != "people" {
		writeJSON(w, http.StatusBadRequest, errorBody("UNSUPPORTED_SEARCH_KIND", "Search kind must be all, posts or people", requestID))
		return
	}
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 50 {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_LIMIT", "Search limit must be between 1 and 50", requestID))
			return
		}
		limit = value
	}
	mediaOnly := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("media_only")), "true")

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var viewer any
	if userID != "" {
		viewer = userID
	}
	posts := make([]homeFeedPost, 0)
	users := make([]map[string]any, 0)

	if kind == "all" || kind == "posts" {
		rows, err := s.db.Query(ctx, `SELECT p.id::text,p.author_id::text,p.text,p.visibility,p.media_ids,
			p.language,p.created_at,p.likes_count,p.replies_count,p.reposts_count,
			a.username,a.display_name,a.avatar_key,a.protected_account,
			(p.author_id=$1::uuid OR EXISTS(SELECT 1 FROM testagram_local.follows f
				WHERE f.follower_id=$1::uuid AND f.following_id=p.author_id AND f.status='accepted')) AS is_following
			FROM testagram_local.posts p
			JOIN testagram_local.accounts a ON a.user_id=p.author_id
			WHERE p.deleted_at IS NULL AND a.account_status='active'
			  AND p.text ILIKE '%' || $2 || '%'
			  AND (NOT $4::boolean OR jsonb_array_length(p.media_ids)>0)
			  AND (
				($1::uuid IS NULL AND p.visibility='public' AND NOT a.protected_account)
				OR ($1::uuid IS NOT NULL AND (p.author_id=$1::uuid
				  OR (p.visibility='public' AND NOT a.protected_account)
				  OR (p.visibility='followers' AND EXISTS(SELECT 1 FROM testagram_local.follows f
					WHERE f.follower_id=$1::uuid AND f.following_id=p.author_id AND f.status='accepted'))))
			  )
			  AND ($1::uuid IS NULL OR NOT EXISTS(SELECT 1 FROM testagram_local.blocks b
				WHERE (b.blocker_id=$1::uuid AND b.blocked_id=p.author_id)
				   OR (b.blocker_id=p.author_id AND b.blocked_id=$1::uuid)))
			  AND ($1::uuid IS NULL OR NOT EXISTS(SELECT 1 FROM testagram_local.mutes m
				WHERE m.muter_id=$1::uuid AND m.muted_id=p.author_id))
			ORDER BY p.created_at DESC,p.id DESC LIMIT $3`, viewer, query, limit, mediaOnly)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_SEARCH_FAILED", "Posts could not be searched", requestID))
			return
		}
		for rows.Next() {
			var post homeFeedPost
			var mediaRaw []byte
			var username, displayName, avatarKey string
			var protected, following bool
			if err := rows.Scan(&post.ID,&post.UserID,&post.Content,&post.Visibility,&mediaRaw,
				&post.Language,&post.CreatedAt,&post.LikesCount,&post.RepliesCount,&post.RepostsCount,
				&username,&displayName,&avatarKey,&protected,&following); err != nil {
				rows.Close()
				writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_SEARCH_FAILED", "Search results could not be decoded", requestID))
				return
			}
			_ = json.Unmarshal(mediaRaw, &post.MediaIDs)
			post.Author = map[string]any{"id":post.UserID,"username":username,"display_name":displayName,"avatar_key":avatarKey,"protected_account":protected}
			post.Origin = "local"
			post.IsFollowing = following
			posts = append(posts, post)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_SEARCH_FAILED", "Search results could not be loaded", requestID))
			return
		}
		rows.Close()
		if err := s.attachHomeFeedMediaURLs(ctx, userID, posts); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("SEARCH_MEDIA_FAILED", "Search media could not be authorized", requestID))
			return
		}
	}

	if kind == "all" || kind == "people" {
		rows, err := s.db.Query(ctx, `SELECT user_id::text,username,display_name,avatar_key
			FROM testagram_local.accounts
			WHERE account_status='active' AND discoverable_by_username=true
			  AND (username ILIKE '%' || $1 || '%' OR display_name ILIKE '%' || $1 || '%')
			ORDER BY lower(username),user_id LIMIT $2`, query, limit)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("PEOPLE_SEARCH_FAILED", "People could not be searched", requestID))
			return
		}
		for rows.Next() {
			var id, username, displayName, avatarKey string
			if err := rows.Scan(&id,&username,&displayName,&avatarKey); err != nil {
				rows.Close()
				writeJSON(w, http.StatusServiceUnavailable, errorBody("PEOPLE_SEARCH_FAILED", "Search results could not be decoded", requestID))
				return
			}
			users = append(users,map[string]any{"id":id,"user_id":id,"username":username,"display_name":displayName,"avatar_url":avatarKey})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			writeJSON(w, http.StatusServiceUnavailable, errorBody("PEOPLE_SEARCH_FAILED", "Search results could not be loaded", requestID))
			return
		}
		rows.Close()
	}
	writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"posts":posts,"users":users,"next_cursor":nil},"error":nil,"request_id":requestID})
}
