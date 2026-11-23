package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type advancedSearchFilters struct {
	From      string
	Since     *time.Time
	Until     *time.Time
	Language  string
	Sort      string
	MediaOnly bool
	VideoOnly bool
}

func nullableAdvancedSearchString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func parseAdvancedSearchFilters(values map[string][]string) (advancedSearchFilters, error) {
	first := func(key string) string {
		if rows := values[key]; len(rows) > 0 {
			return strings.TrimSpace(rows[0])
		}
		return ""
	}
	filters := advancedSearchFilters{Sort: "latest"}
	filters.From = strings.TrimPrefix(first("from"), "@")
	if filters.From != "" {
		if len(filters.From) < 2 || len(filters.From) > 30 {
			return filters, errors.New("from must be a valid username")
		}
		for _, r := range filters.From {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
				(r >= '0' && r <= '9') || r == '_' || r == '.') {
				return filters, errors.New("from must be a valid username")
			}
		}
	}

	parseDate := func(raw string, exclusiveDate bool) (*time.Time, error) {
		if raw == "" {
			return nil, nil
		}
		var parsed time.Time
		var err error
		if len(raw) == len("2006-01-02") {
			parsed, err = time.Parse("2006-01-02", raw)
			if err == nil && exclusiveDate {
				parsed = parsed.Add(24 * time.Hour)
			}
		} else {
			parsed, err = time.Parse(time.RFC3339, raw)
		}
		if err != nil {
			return nil, errors.New("dates must use YYYY-MM-DD or RFC3339")
		}
		parsed = parsed.UTC()
		return &parsed, nil
	}

	var err error
	filters.Since, err = parseDate(first("since"), false)
	if err != nil {
		return filters, err
	}
	filters.Until, err = parseDate(first("until"), true)
	if err != nil {
		return filters, err
	}
	if filters.Since != nil && filters.Until != nil && !filters.Since.Before(*filters.Until) {
		return filters, errors.New("since must be earlier than until")
	}

	filters.Language = strings.ToLower(first("language"))
	if filters.Language != "" {
		if len(filters.Language) > 35 {
			return filters, errors.New("language must be a valid language tag")
		}
		for _, r := range filters.Language {
			if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
				return filters, errors.New("language must be a valid language tag")
			}
		}
	}

	filters.Sort = strings.ToLower(first("sort"))
	if filters.Sort == "" {
		filters.Sort = "latest"
	}
	if filters.Sort != "latest" && filters.Sort != "popular" {
		return filters, errors.New("sort must be latest or popular")
	}

	parseBool := func(key string) (bool, error) {
		raw := first(key)
		if raw == "" {
			return false, nil
		}
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return false, errors.New(key + " must be true or false")
		}
		return value, nil
	}
	filters.MediaOnly, err = parseBool("media_only")
	if err != nil {
		return filters, err
	}
	filters.VideoOnly, err = parseBool("video_only")
	if err != nil {
		return filters, err
	}
	return filters, nil
}

func (s *service) advancedSearchAPI(w http.ResponseWriter, r *http.Request) {
	userID, requestID, ok := s.socialOptionalReadAuth(w, r)
	if !ok {
		return
	}
	searchText := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(searchText) < 2 || len(searchText) > 120 {
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
	filters, err := parseAdvancedSearchFilters(r.URL.Query())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_SEARCH_FILTER", err.Error(), requestID))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var viewer any
	if userID != "" {
		viewer = userID
	}
	posts := make([]homeFeedPost, 0)
	users := make([]map[string]any, 0)

	if kind == "all" || kind == "posts" {
		orderBy := "p.created_at DESC,p.id DESC"
		if filters.Sort == "popular" {
			orderBy = "(p.likes_count + 2*p.replies_count + 2*p.reposts_count) DESC,p.created_at DESC,p.id DESC"
		}
		postsSQL := `SELECT p.id::text,p.author_id::text,p.text,p.visibility,p.media_ids,
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
			  AND ($5::text IS NULL OR lower(a.username)=lower($5))
			  AND ($6::timestamptz IS NULL OR p.created_at >= $6)
			  AND ($7::timestamptz IS NULL OR p.created_at < $7)
			  AND ($8::text IS NULL OR lower(p.language)=lower($8))
			  AND (NOT $9::boolean OR EXISTS (
				SELECT 1
				FROM jsonb_array_elements_text(COALESCE(p.media_ids,'[]'::jsonb)) AS media_key(object_key)
				JOIN testagram_media.assets ma
				  ON ma.object_key=media_key.object_key
				 AND ma.owner_id=p.author_id
				 AND ma.status='ready'
				 AND ma.visibility='public'
				 AND ma.content_type LIKE 'video/%'
			  ))
			ORDER BY ` + orderBy + ` LIMIT $3`
		rows, err := s.db.Query(ctx, postsSQL, viewer, searchText, limit, filters.MediaOnly,
			nullableAdvancedSearchString(filters.From), filters.Since, filters.Until,
			nullableAdvancedSearchString(filters.Language), filters.VideoOnly)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_SEARCH_FAILED", "Posts could not be searched", requestID))
			return
		}
		for rows.Next() {
			var post homeFeedPost
			var mediaRaw []byte
			var username, displayName, avatarKey string
			var protected, following bool
			if err := rows.Scan(&post.ID, &post.UserID, &post.Content, &post.Visibility, &mediaRaw,
				&post.Language, &post.CreatedAt, &post.LikesCount, &post.RepliesCount, &post.RepostsCount,
				&username, &displayName, &avatarKey, &protected, &following); err != nil {
				rows.Close()
				writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_SEARCH_FAILED", "Search results could not be decoded", requestID))
				return
			}
			_ = json.Unmarshal(mediaRaw, &post.MediaIDs)
			post.Author = map[string]any{
				"id": post.UserID, "username": username, "display_name": displayName,
				"avatar_key": avatarKey, "protected_account": protected,
			}
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
			ORDER BY lower(username),user_id LIMIT $2`, searchText, limit)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("PEOPLE_SEARCH_FAILED", "People could not be searched", requestID))
			return
		}
		for rows.Next() {
			var id, username, displayName, avatarKey string
			if err := rows.Scan(&id, &username, &displayName, &avatarKey); err != nil {
				rows.Close()
				writeJSON(w, http.StatusServiceUnavailable, errorBody("PEOPLE_SEARCH_FAILED", "Search results could not be decoded", requestID))
				return
			}
			users = append(users, map[string]any{
				"id": id, "user_id": id, "username": username,
				"display_name": displayName, "avatar_url": avatarKey,
			})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			writeJSON(w, http.StatusServiceUnavailable, errorBody("PEOPLE_SEARCH_FAILED", "Search results could not be loaded", requestID))
			return
		}
		rows.Close()
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"data": map[string]any{
			"posts": posts, "users": users, "next_cursor": nil,
			"filters": map[string]any{
				"from": filters.From, "since": filters.Since, "until": filters.Until,
				"language": filters.Language, "sort": filters.Sort,
				"media_only": filters.MediaOnly, "video_only": filters.VideoOnly,
			},
		},
		"error": nil, "request_id": requestID,
	})
}
