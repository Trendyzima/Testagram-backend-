package main

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/algorithmclient"
)

// publicHomeFeed is the anonymous discovery surface. It can return only
// published public posts from active, non-protected accounts. It deliberately
// has no viewer-specific relationship state and never queries Supabase.
func (s *service) publicHomeFeed(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	w.Header().Set("Cache-Control", "public, max-age=10, stale-while-revalidate=30")
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 50 {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_LIMIT", "Feed limit must be between 1 and 50", requestID))
			return
		}
		limit = parsed
	}
	cursor, err := decodeHomeFeedCursor(r.URL.Query().Get("before"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_CURSOR", "Feed cursor is invalid", requestID))
		return
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("FEED_STORE_UNAVAILABLE", "Feed storage is unavailable", requestID))
		return
	}

	queryCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	query := `
SELECT p.id::text, p.author_id::text, p.text, p.visibility, p.media_ids,
       p.language, p.created_at, p.likes_count, p.replies_count, p.reposts_count,
       a.username, a.display_name, a.avatar_key, a.protected_account
FROM testagram_local.posts p
JOIN testagram_local.accounts a ON a.user_id=p.author_id
WHERE p.deleted_at IS NULL
  AND p.visibility='public'
  AND a.account_status='active'
  AND NOT a.protected_account
  AND ($1::timestamptz IS NULL OR (p.created_at,p.id) < ($1::timestamptz,$2::uuid))
ORDER BY p.created_at DESC, p.id DESC
LIMIT $3`
	var before any
	var beforeID any
	if cursor != nil {
		before, beforeID = cursor.CreatedAt, cursor.ID
	}
	rows, err := s.db.Query(queryCtx, query, before, beforeID, limit+1)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("FEED_QUERY_FAILED", "Public feed could not be loaded", requestID))
		return
	}
	defer rows.Close()

	posts := make([]homeFeedPost, 0, limit)
	candidates := make([]algorithmclient.Candidate, 0, limit)
	hasMore := false
	for rows.Next() {
		var post homeFeedPost
		var mediaRaw []byte
		var username, displayName, avatarKey string
		var protected bool
		if err := rows.Scan(&post.ID, &post.UserID, &post.Content, &post.Visibility, &mediaRaw,
			&post.Language, &post.CreatedAt, &post.LikesCount, &post.RepliesCount, &post.RepostsCount,
			&username, &displayName, &avatarKey, &protected); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("FEED_SCAN_FAILED", "Public feed could not be loaded", requestID))
			return
		}
		if len(posts) == limit {
			hasMore = true
			break
		}
		post.MediaIDs = []string{}
		_ = json.Unmarshal(mediaRaw, &post.MediaIDs)
		post.Author = map[string]any{"id": post.UserID, "username": username, "display_name": displayName, "avatar_key": avatarKey, "protected_account": protected}
		post.Origin = "local"
		quality := math.Min(1, float64(post.LikesCount+2*post.RepliesCount+2*post.RepostsCount)/50)
		conversation := math.Min(1, float64(post.RepliesCount)/20)
		posts = append(posts, post)
		candidates = append(candidates, algorithmclient.Candidate{
			ID: post.ID, AuthorID: post.UserID, CreatedAt: post.CreatedAt, Visible: true,
			Features: algorithmclient.Features{Following: false, Quality: quality, Conversation: conversation},
		})
	}
	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("FEED_QUERY_FAILED", "Public feed could not be loaded", requestID))
		return
	}

	rankingMode := "chronological"
	if s.algorithm != nil && len(candidates) > 0 {
		s.algorithmRequests.Add(1)
		rankCtx, rankCancel := context.WithTimeout(r.Context(), 1400*time.Millisecond)
		ranked, rankErr := s.algorithm.Rank(rankCtx, algorithmclient.Request{
			Now: time.Now().UTC(), Candidates: candidates, Limit: limit, AllowSensitive: false,
		})
		rankCancel()
		if rankErr == nil {
			byID := make(map[string]homeFeedPost, len(posts))
			for _, post := range posts {
				byID[post.ID] = post
			}
			ordered := make([]homeFeedPost, 0, len(ranked.Items))
			used := make(map[string]struct{}, len(ranked.Items))
			for _, item := range ranked.Items {
				post, ok := byID[item.ID]
				if !ok {
					continue
				}
				if _, duplicate := used[item.ID]; duplicate {
					continue
				}
				used[item.ID] = struct{}{}
				post.RankingScore = item.Score
				post.RankingReasons = item.Reasons
				ordered = append(ordered, post)
			}
			posts = ordered
			rankingMode = "algorithm"
		} else {
			s.algorithmFailures.Add(1)
		}
	}
	if err := s.attachHomeFeedMediaURLs(r.Context(), "", posts); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("FEED_MEDIA_FAILED", "Public feed media could not be authorized", requestID))
		return
	}

	var nextCursor string
	if hasMore && len(candidates) > 0 {
		last := candidates[len(candidates)-1]
		nextCursor = encodeHomeFeedCursor(homeFeedCursor{CreatedAt: last.CreatedAt, ID: last.ID})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"data": map[string]any{"posts": posts, "next_cursor": nextCursor, "has_more": hasMore, "ranking_mode": rankingMode},
		"error": nil,
		"request_id": requestID,
	})
}
