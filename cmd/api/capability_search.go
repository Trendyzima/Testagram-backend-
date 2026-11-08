package main

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (s *service) nativeUnifiedSearch(w http.ResponseWriter, r *http.Request, requestID string, input map[string]any) bool {
	if strings.TrimSpace(r.Header.Get("Authorization")) == "" {
		return false
	}
	userID, status, err := s.authenticatedMediaUser(r)
	if err != nil {
		message := "A valid authenticated session is required"
		if status >= 500 { message = "Authentication service is temporarily unavailable" }
		writeJSON(w, status, errorBody("AUTH_REQUIRED", message, requestID))
		return true
	}
	q, _ := input["q"].(string)
	q = strings.TrimSpace(q)
	if len(q) > 512 {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_SEARCH_INPUT", "Search query exceeds 512 characters", requestID))
		return true
	}
	kind, _ := input["kind"].(string)
	if kind == "" { kind = "all" }
	switch kind {
	case "all", "people", "posts", "hashtags", "threads", "communities", "fediverse", "media", "latest", "top", "instances":
	default:
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_SEARCH_INPUT", "Unsupported search category", requestID))
		return true
	}
	readInt := func(key string, fallback, min, max int) int {
		value, ok := input[key]
		if !ok || value == nil { return fallback }
		parsed, parseErr := strconv.Atoi(strings.TrimSpace(toString(value)))
		if parseErr != nil || parsed < min || parsed > max { return -1 }
		return parsed
	}
	limit := readInt("limit", 40, 1, 100)
	offset := readInt("offset", 0, 0, 100000)
	if limit < 0 || offset < 0 {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_SEARCH_INPUT", "Search pagination is outside the allowed bounds", requestID))
		return true
	}
	sortMode, _ := input["sort"].(string)
	if sortMode == "" { sortMode = "relevance" }
	switch sortMode {
	case "relevance", "latest", "most_liked", "most_viewed":
	default:
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_SEARCH_INPUT", "Unsupported search sort", requestID))
		return true
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("DATABASE_UNAVAILABLE", "Search database is unavailable", requestID))
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	users := make([]map[string]any, 0)
	posts := make([]map[string]any, 0)
	if q != "" && (kind == "all" || kind == "people") {
		rows, queryErr := s.db.Query(ctx, `SELECT a.user_id::text,a.username,a.display_name,a.bio,a.avatar_key,a.follower_count
			FROM testagram_local.accounts a
			WHERE a.account_status='active' AND a.discoverable_by_username=TRUE AND a.protected_account=FALSE
			  AND (a.username ILIKE $1 OR a.display_name ILIKE $1 OR a.bio ILIKE $1)
			  AND NOT EXISTS (SELECT 1 FROM testagram_local.blocks b WHERE
			    (b.blocker_id=$2::uuid AND b.blocked_id=a.user_id) OR (b.blocker_id=a.user_id AND b.blocked_id=$2::uuid))
			ORDER BY CASE WHEN lower(a.username)=lower($3) THEN 0 ELSE 1 END,a.follower_count DESC,a.username
			OFFSET $4 LIMIT $5`, "%"+q+"%", userID, q, offset, limit)
		if queryErr != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("SEARCH_UNAVAILABLE", "People search is temporarily unavailable", requestID))
			return true
		}
		for rows.Next() {
			var id, username, displayName, bio, avatar string
			var followers int64
			if scanErr := rows.Scan(&id,&username,&displayName,&bio,&avatar,&followers); scanErr != nil {
				rows.Close(); writeJSON(w,http.StatusServiceUnavailable,errorBody("SEARCH_UNAVAILABLE","People search is temporarily unavailable",requestID)); return true
			}
			users = append(users,map[string]any{"id":id,"user_id":id,"username":username,"display_name":displayName,"bio":bio,"avatar_key":avatar,"followers_count":followers,"follower_count":followers})
		}
		if rows.Err() != nil { rows.Close(); writeJSON(w,http.StatusServiceUnavailable,errorBody("SEARCH_UNAVAILABLE","People search is temporarily unavailable",requestID)); return true }
		rows.Close()
	}
	if q != "" && (kind == "all" || kind == "posts" || kind == "latest" || kind == "top" || kind == "media") {
		order := "p.created_at DESC,p.id DESC"
		if sortMode == "most_liked" || kind == "top" { order = "p.likes_count DESC,p.created_at DESC,p.id DESC" }
		// The order expression is selected from fixed constants; all user input remains parameterized.
		query := `SELECT p.id::text,p.author_id::text,p.text,p.visibility,p.media_ids,p.language,p.created_at,
			a.username,a.display_name,a.avatar_key,p.likes_count,p.replies_count,p.reposts_count
			FROM testagram_local.posts p JOIN testagram_local.accounts a ON a.user_id=p.author_id
			WHERE p.deleted_at IS NULL AND p.visibility='public' AND a.account_status='active'
			  AND (p.text ILIKE $1 OR a.username ILIKE $1 OR a.display_name ILIKE $1)
			  AND NOT EXISTS (SELECT 1 FROM testagram_local.blocks b WHERE
			    (b.blocker_id=$2::uuid AND b.blocked_id=p.author_id) OR (b.blocker_id=p.author_id AND b.blocked_id=$2::uuid))
			ORDER BY `+order+` OFFSET $3 LIMIT $4
		rows, queryErr := s.db.Query(ctx, query, "%"+q+"%", userID, offset, limit)
		if queryErr != nil {
			writeJSON(w,http.StatusServiceUnavailable,errorBody("SEARCH_UNAVAILABLE","Post search is temporarily unavailable",requestID)); return true
		}
		for rows.Next() {
			var id, authorID, body, visibility, language, username, displayName, avatar string
			var media []byte
			var createdAt time.Time
			var likes,replies,reposts int64
			if scanErr := rows.Scan(&id,&authorID,&body,&visibility,&media,&language,&createdAt,&username,&displayName,&avatar,&likes,&replies,&reposts); scanErr != nil {
				rows.Close(); writeJSON(w,http.StatusServiceUnavailable,errorBody("SEARCH_UNAVAILABLE","Post search is temporarily unavailable",requestID)); return true
			}
			posts=append(posts,map[string]any{"id":id,"post_id":id,"author_id":authorID,"content":body,"text":body,"visibility":visibility,"media_ids":media,"language":language,"created_at":createdAt,"author":map[string]any{"id":authorID,"username":username,"display_name":displayName,"avatar_key":avatar},"likes_count":likes,"replies_count":replies,"reposts_count":reposts})
		}
		if rows.Err()!=nil { rows.Close(); writeJSON(w,http.StatusServiceUnavailable,errorBody("SEARCH_UNAVAILABLE","Post search is temporarily unavailable",requestID)); return true }
		rows.Close()
	}
	// This initial Neon search slice deliberately reports unsupported indexed
	// categories as empty rather than querying legacy Supabase tables.
	writeJSON(w,http.StatusOK,map[string]any{"ok":true,"data":map[string]any{"users":users,"posts":posts,"hashtags":[]any{},"threads":[]any{},"communities":[]any{},"replies":[]any{},"fediverse":[]any{},"instances":[]any{},"next_cursor":nil,"partial":kind!="all" && kind!="people" && kind!="posts" && kind!="latest" && kind!="top" && kind!="media"},"error":nil,"request_id":requestID})
	return true
}
