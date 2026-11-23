package main

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/algorithmclient"
)

var trendingWordStopwords = []string{
	"about", "after", "again", "also", "and", "are", "because", "been",
	"before", "being", "but", "can", "could", "did", "does", "doing",
	"for", "from", "had", "has", "have", "her", "here", "him", "his",
	"how", "into", "its", "just", "more", "most", "not", "our", "out",
	"over", "she", "should", "some", "such", "than", "that", "the",
	"their", "them", "then", "there", "these", "they", "this", "those",
	"through", "too", "under", "very", "was", "were", "what", "when",
	"where", "which", "who", "why", "will", "with", "would", "you",
	"your", "https", "http", "www",
}

// socialTrendingWords counts distinct public posts in a rolling seven-day
// window. The Backend discovers only public, non-protected content; Algorithm
// ranks the bounded candidate set. No private text enters this pipeline.
func (s *service) socialTrendingWords(w http.ResponseWriter, r *http.Request) {
	_, requestID, ok := s.socialOptionalReadAuth(w, r)
	if !ok {
		return
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("TRENDING_WORDS_STORE_UNAVAILABLE", "Trending words are temporarily unavailable", requestID))
		return
	}
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 50 {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_LIMIT", "Trending word limit must be between 1 and 50", requestID))
			return
		}
		limit = value
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	rows, err := s.db.Query(ctx, `
SELECT lower(parts[1]) AS word,
       (count(DISTINCT p.id) FILTER (WHERE p.created_at >= now()-interval '7 days'))::bigint AS posts_count,
       (count(DISTINCT p.id) FILTER (WHERE p.created_at < now()-interval '7 days'))::bigint AS previous_posts_count,
       max(p.created_at) FILTER (WHERE p.created_at >= now()-interval '7 days') AS last_used_at
FROM testagram_local.posts p
JOIN testagram_local.accounts a ON a.user_id=p.author_id
CROSS JOIN LATERAL regexp_matches(lower(p.text),'([[:alpha:]][[:alnum:]_]{2,})','g') AS extracted(parts)
WHERE p.deleted_at IS NULL
  AND p.visibility='public'
  AND a.account_status='active'
  AND NOT a.protected_account
  AND p.created_at >= now()-interval '14 days'
  AND lower(parts[1]) <> ALL($1::text[])
  AND lower(parts[1]) !~ '^[0-9_]+`, trendingWordStopwords)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("TRENDING_WORDS_FAILED", "Trending words could not be loaded", requestID))
		return
	}
	defer rows.Close()

	candidates := make([]algorithmclient.TopicCandidate, 0, 200)
	fallback := make([]map[string]any, 0, limit)
	for rows.Next() {
		var word string
		var count, previousCount int64
		var lastUsed time.Time
		if err := rows.Scan(&word, &count, &previousCount, &lastUsed); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("TRENDING_WORDS_FAILED", "Trending words could not be decoded", requestID))
			return
		}
		candidates = append(candidates, algorithmclient.TopicCandidate{
			Tag: word, PostsCount: count, LastUsedAt: lastUsed,
			Momentum: trendMomentum(count, previousCount),
		})
		if len(fallback) < limit {
			fallback = append(fallback, map[string]any{
				"word": word, "posts_count": count,
				"last_used_at": lastUsed.UTC().Format(time.RFC3339Nano),
				"momentum": trendMomentum(count, previousCount),
			})
		}
	}
	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("TRENDING_WORDS_FAILED", "Trending words could not be loaded", requestID))
		return
	}

	items := fallback
	rankingMode := "recent_public_post_volume"
	if s.algorithm != nil && len(candidates) > 0 {
		s.algorithmRequests.Add(1)
		ranked, rankErr := s.algorithm.RankTopics(ctx, algorithmclient.TopicRequest{
			Now: time.Now().UTC(), Candidates: candidates, Limit: limit,
		})
		if rankErr == nil && len(ranked.Items) > 0 {
			items = make([]map[string]any, 0, len(ranked.Items))
			for _, item := range ranked.Items {
				items = append(items, map[string]any{
					"word": item.Tag, "posts_count": item.PostsCount,
					"score": item.Score, "reasons": item.Reasons,
				})
			}
			rankingMode = "algorithm_topic_ranker_v1"
		} else {
			s.algorithmFailures.Add(1)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"data": map[string]any{
			"items": items, "window": "7d", "ranking": rankingMode,
			"minimum_posts": 2,
		},
		"error": nil, "request_id": requestID,
	})
}

GROUP BY lower(parts[1])
HAVING count(DISTINCT p.id) FILTER (WHERE p.created_at >= now()-interval '7 days') >= 2
ORDER BY posts_count DESC,last_used_at DESC,lower(parts[1])
LIMIT 200`, trendingWordStopwords)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("TRENDING_WORDS_FAILED", "Trending words could not be loaded", requestID))
		return
	}
	defer rows.Close()

	candidates := make([]algorithmclient.TopicCandidate, 0, 200)
	fallback := make([]map[string]any, 0, limit)
	for rows.Next() {
		var word string
		var count int64
		var lastUsed time.Time
		if err := rows.Scan(&word, &count, &lastUsed); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("TRENDING_WORDS_FAILED", "Trending words could not be decoded", requestID))
			return
		}
		candidates = append(candidates, algorithmclient.TopicCandidate{
			Tag: word, PostsCount: count, LastUsedAt: lastUsed,
		})
		if len(fallback) < limit {
			fallback = append(fallback, map[string]any{
				"word": word, "posts_count": count,
				"last_used_at": lastUsed.UTC().Format(time.RFC3339Nano),
			})
		}
	}
	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("TRENDING_WORDS_FAILED", "Trending words could not be loaded", requestID))
		return
	}

	items := fallback
	rankingMode := "recent_public_post_volume"
	if s.algorithm != nil && len(candidates) > 0 {
		s.algorithmRequests.Add(1)
		ranked, rankErr := s.algorithm.RankTopics(ctx, algorithmclient.TopicRequest{
			Now: time.Now().UTC(), Candidates: candidates, Limit: limit,
		})
		if rankErr == nil && len(ranked.Items) > 0 {
			items = make([]map[string]any, 0, len(ranked.Items))
			for _, item := range ranked.Items {
				items = append(items, map[string]any{
					"word": item.Tag, "posts_count": item.PostsCount,
					"score": item.Score, "reasons": item.Reasons,
				})
			}
			rankingMode = "algorithm_topic_ranker_v1"
		} else {
			s.algorithmFailures.Add(1)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"data": map[string]any{
			"items": items, "window": "7d", "ranking": rankingMode,
			"minimum_posts": 2,
		},
		"error": nil, "request_id": requestID,
	})
}
