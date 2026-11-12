package main

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/algorithmclient"
)

var hashtagNamePattern = regexp.MustCompile("^[\\p{L}\\p{N}_]{1,64}$")

func normalizeHashtag(value string) (string, string, bool) {
	value = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), "#"))
	if !hashtagNamePattern.MatchString(value) {
		return "", "", false
	}
	tag := strings.ToLower(value)
	pattern := "(^|[^[:alnum:]_])#" + regexp.QuoteMeta(tag) + "([^[:alnum:]_]|$)"
	return tag, pattern, true
}

// socialHashtagRead returns a privacy-filtered timeline for one normalized tag.
// The Backend remains authoritative for visibility, blocks, mutes and media access.
func (s *service) socialHashtagRead(w http.ResponseWriter, r *http.Request) {
	requestID := requestID(r)
	tag, pattern, valid := normalizeHashtag(r.PathValue("tag"))
	if !valid {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_HASHTAG", "Hashtag must contain 1-64 letters, numbers or underscores", requestID))
		return
	}
	viewerID, requestID, ok := s.socialOptionalReadAuth(w, r)
	if !ok {
		return
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("HASHTAG_STORE_UNAVAILABLE", "Hashtag posts are temporarily unavailable", requestID))
		return
	}
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 50 {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_LIMIT", "Hashtag page limit must be between 1 and 50", requestID))
			return
		}
		limit = value
	}
	cursor, err := decodeHomeFeedCursor(r.URL.Query().Get("before"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_CURSOR", "Hashtag page cursor is invalid", requestID))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	var before any
	var beforeID any
	if cursor != nil {
		before, beforeID = cursor.CreatedAt, cursor.ID
	}
	rows, err := s.db.Query(ctx, `
SELECT p.id::text,p.author_id::text,p.text,p.visibility,p.media_ids,
       p.language,p.created_at,p.likes_count,p.replies_count,p.reposts_count,
       a.username,a.display_name,a.avatar_key,a.protected_account
FROM testagram_local.posts p
JOIN testagram_local.accounts a ON a.user_id=p.author_id
WHERE p.deleted_at IS NULL
  AND a.account_status='active'
  AND p.text ~* $2
  AND (
    ($1::uuid IS NULL AND p.visibility='public' AND NOT a.protected_account)
    OR ($1::uuid IS NOT NULL AND (
      p.author_id=$1::uuid
      OR (p.visibility='public' AND NOT a.protected_account)
      OR (p.visibility='followers' AND EXISTS(
        SELECT 1 FROM testagram_local.follows f
        WHERE f.follower_id=$1::uuid AND f.following_id=p.author_id AND f.status='accepted'
      ))
    ))
  )
  AND ($1::uuid IS NULL OR NOT EXISTS(
    SELECT 1 FROM testagram_local.blocks b
    WHERE (b.blocker_id=$1::uuid AND b.blocked_id=p.author_id)
       OR (b.blocker_id=p.author_id AND b.blocked_id=$1::uuid)
  ))
  AND ($1::uuid IS NULL OR NOT EXISTS(
    SELECT 1 FROM testagram_local.mutes m
    WHERE m.muter_id=$1::uuid AND m.muted_id=p.author_id
  ))
  AND ($3::timestamptz IS NULL OR (p.created_at,p.id)<($3::timestamptz,$4::uuid))
ORDER BY p.created_at DESC,p.id DESC
LIMIT $5`, nullableUUID(viewerID), pattern, before, beforeID, limit+1)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("HASHTAG_QUERY_FAILED", "Hashtag posts could not be loaded", requestID))
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
			writeJSON(w, http.StatusServiceUnavailable, errorBody("HASHTAG_QUERY_FAILED", "Hashtag posts could not be decoded", requestID))
			return
		}
		if len(posts) == limit {
			hasMore = true
			break
		}
		post.MediaIDs = []string{}
		_ = json.Unmarshal(mediaRaw, &post.MediaIDs)
		post.Author = map[string]any{"id":post.UserID,"username":username,"display_name":displayName,
			"avatar_key":avatarKey,"avatar_url":publicMediaURL(avatarKey),"protected_account":protected}
		post.Origin = "local"
		posts = append(posts, post)
		next = homeFeedCursor{CreatedAt:post.CreatedAt,ID:post.ID}
	}
	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("HASHTAG_QUERY_FAILED", "Hashtag posts could not be loaded", requestID))
		return
	}
	if err := s.attachHomeFeedMediaURLs(ctx, viewerID, posts); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("HASHTAG_MEDIA_UNAVAILABLE", "Hashtag post media could not be authorized", requestID))
		return
	}
	var nextCursor string
	if hasMore && len(posts) > 0 {
		nextCursor = encodeHomeFeedCursor(next)
	}
	isFollowing := false
	if viewerID != "" {
		if err := s.db.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM testagram_local.hashtag_follows WHERE user_id=$1::uuid AND tag=$2
		)`, viewerID, tag).Scan(&isFollowing); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("HASHTAG_FOLLOW_STATE_FAILED", "Hashtag follow state could not be loaded", requestID))
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"data": map[string]any{"tag": tag, "posts": posts, "has_more": hasMore, "next_cursor": nextCursor, "is_following": isFollowing},
		"error": nil, "request_id": requestID,
	})
}


// socialHashtagFollow adds a private viewer-interest signal for Explore ranking.
func (s *service) socialHashtagFollow(w http.ResponseWriter, r *http.Request) {
	userID, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	tag, _, valid := normalizeHashtag(r.PathValue("tag"))
	if !valid {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_HASHTAG", "Hashtag must contain 1-64 letters, numbers or underscores", requestID))
		return
	}
	if !s.socialWriteAllowed(w, r, userID, requestID, "hashtag_follow", 60) {
		return
	}
	if _, err := s.db.Exec(r.Context(), `
INSERT INTO testagram_local.hashtag_follows(user_id,tag)
VALUES($1::uuid,$2)
ON CONFLICT(user_id,tag) DO NOTHING`, userID, tag); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("HASHTAG_FOLLOW_FAILED", "Could not follow this hashtag", requestID))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "data": map[string]any{"tag": tag, "is_following": true},
		"error": nil, "request_id": requestID,
	})
}

func (s *service) socialHashtagUnfollow(w http.ResponseWriter, r *http.Request) {
	userID, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	tag, _, valid := normalizeHashtag(r.PathValue("tag"))
	if !valid {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_HASHTAG", "Hashtag must contain 1-64 letters, numbers or underscores", requestID))
		return
	}
	if !s.socialWriteAllowed(w, r, userID, requestID, "hashtag_unfollow", 60) {
		return
	}
	if _, err := s.db.Exec(r.Context(), `
DELETE FROM testagram_local.hashtag_follows WHERE user_id=$1::uuid AND tag=$2`, userID, tag); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("HASHTAG_UNFOLLOW_FAILED", "Could not unfollow this hashtag", requestID))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "data": map[string]any{"tag": tag, "is_following": false},
		"error": nil, "request_id": requestID,
	})
}

func (s *service) socialHashtagFollowState(w http.ResponseWriter, r *http.Request) {
	userID, requestID, ok := s.socialOptionalReadAuth(w, r)
	if !ok {
		return
	}
	tag, _, valid := normalizeHashtag(r.PathValue("tag"))
	if !valid {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_HASHTAG", "Hashtag must contain 1-64 letters, numbers or underscores", requestID))
		return
	}
	isFollowing := false
	if userID != "" {
		if err := s.db.QueryRow(r.Context(), `
SELECT EXISTS(SELECT 1 FROM testagram_local.hashtag_follows WHERE user_id=$1::uuid AND tag=$2)`,
			userID, tag).Scan(&isFollowing); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("HASHTAG_FOLLOW_STATE_FAILED", "Hashtag follow state could not be loaded", requestID))
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "data": map[string]any{"tag": tag, "is_following": isFollowing},
		"error": nil, "request_id": requestID,
	})
}

// socialTrendingHashtags ranks public hashtags by distinct-post volume over
// the last seven days. This is a safe baseline until Algorithm owns topic ranking.
func (s *service) socialTrendingHashtags(w http.ResponseWriter, r *http.Request) {
	viewerID, requestID, ok := s.socialOptionalReadAuth(w, r)
	if !ok {
		return
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("HASHTAG_STORE_UNAVAILABLE", "Trending topics are temporarily unavailable", requestID))
		return
	}
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 50 {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_LIMIT", "Trending hashtag limit must be between 1 and 50", requestID))
			return
		}
		limit = value
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	rows, err := s.db.Query(ctx, `
SELECT lower(parts[1]) AS tag,count(DISTINCT p.id)::bigint AS posts_count,
       max(p.created_at) AS last_used_at,
       COALESCE(bool_or(hf.user_id IS NOT NULL),false) AS is_following
FROM testagram_local.posts p
JOIN testagram_local.accounts a ON a.user_id=p.author_id
CROSS JOIN LATERAL regexp_matches(p.text,'#([[:alnum:]_]{1,64})','g') AS extracted(parts)
LEFT JOIN testagram_local.hashtag_follows hf
  ON hf.user_id=$1::uuid AND hf.tag=lower(parts[1])
WHERE p.deleted_at IS NULL
  AND p.visibility='public'
  AND a.account_status='active'
  AND NOT a.protected_account
  AND p.created_at >= now()-interval '7 days'
GROUP BY lower(parts[1])
ORDER BY count(DISTINCT p.id) DESC,max(p.created_at) DESC,lower(parts[1])
LIMIT 200`, nullableUUID(viewerID))
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("TRENDING_HASHTAGS_FAILED", "Trending topics could not be loaded", requestID))
		return
	}
	defer rows.Close()
	topicCandidates := make([]algorithmclient.TopicCandidate, 0, 200)
	fallbackItems := make([]map[string]any, 0, limit)
	topicFollowState := make(map[string]bool, 200)
	for rows.Next() {
		var tag string
		var count int64
		var lastUsed time.Time
		var isFollowing bool
		if err := rows.Scan(&tag, &count, &lastUsed, &isFollowing); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("TRENDING_HASHTAGS_FAILED", "Trending topics could not be decoded", requestID))
			return
		}
		affinity := 0.0
		if isFollowing {
			affinity = 1
		}
		topicCandidates = append(topicCandidates, algorithmclient.TopicCandidate{
			Tag: tag, PostsCount: count, LastUsedAt: lastUsed, InterestAffinity: affinity,
		})
		topicFollowState[tag] = isFollowing
		if len(fallbackItems) < limit {
			fallbackItems = append(fallbackItems, map[string]any{
				"tag": tag, "posts_count": count, "last_used_at": lastUsed.UTC().Format(time.RFC3339Nano), "is_following": isFollowing,
			})
		}
	}
	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("TRENDING_HASHTAGS_FAILED", "Trending topics could not be loaded", requestID))
		return
	}
	items := fallbackItems
	rankingMode := "recent_post_volume"
	if s.algorithm != nil && len(topicCandidates) > 0 {
		s.algorithmRequests.Add(1)
		ranked, rankErr := s.algorithm.RankTopics(ctx, algorithmclient.TopicRequest{
			Now: time.Now().UTC(), Candidates: topicCandidates, Limit: limit,
		})
		if rankErr == nil && len(ranked.Items) > 0 {
			items = make([]map[string]any, 0, len(ranked.Items))
			for _, topic := range ranked.Items {
				items = append(items, map[string]any{
					"tag": topic.Tag, "posts_count": topic.PostsCount, "score": topic.Score, "reasons": topic.Reasons, "is_following": topicFollowState[topic.Tag],
				})
			}
			rankingMode = "algorithm_topic_ranker_v1"
		} else {
			s.algorithmFailures.Add(1)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"data": map[string]any{"items": items, "window": "7d", "ranking": rankingMode},
		"error": nil, "request_id": requestID,
	})
}

func nullableUUID(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
