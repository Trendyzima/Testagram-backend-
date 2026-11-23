package main

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const knowledgeMaxQuestionTopics = 5

func normalizeKnowledgeTopicSlug(value string) (string, bool) {
	slug := strings.ToLower(strings.TrimSpace(value))
	if len(slug) < 2 || len(slug) > 63 {
		return "", false
	}
	for i, r := range slug {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || (i > 0 && r == '-')
		if !valid {
			return "", false
		}
	}
	return slug, true
}

func normalizeKnowledgeTopicSlugs(values []string) ([]string, bool) {
	if len(values) > knowledgeMaxQuestionTopics {
		return nil, false
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		slug, ok := normalizeKnowledgeTopicSlug(value)
		if !ok {
			return nil, false
		}
		if _, exists := seen[slug]; exists {
			continue
		}
		seen[slug] = struct{}{}
		out = append(out, slug)
	}
	sort.Strings(out)
	return out, true
}

func (s *service) knowledgeTopics(w http.ResponseWriter, r *http.Request) {
	uid, rid, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	limit := 30
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 50 {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_LIMIT", "Limit must be 1-50", rid))
			return
		}
		limit = n
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(query)) > 80 {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_QUERY", "Topic search must be at most 80 characters", rid))
		return
	}
	after := ""
	if raw := strings.TrimSpace(r.URL.Query().Get("after")); raw != "" {
		var valid bool
		after, valid = normalizeKnowledgeTopicSlug(raw)
		if !valid {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_CURSOR", "Topic cursor is invalid", rid))
			return
		}
	}
	rows, err := s.db.Query(ctx, `
SELECT t.id::text,t.slug,t.name,t.description,
       (SELECT count(*) FROM testagram_local.topic_follows tf JOIN testagram_local.accounts follower ON follower.user_id=tf.user_id WHERE tf.topic_id=t.id AND follower.account_status='active')::bigint,
       (SELECT count(*) FROM testagram_local.question_topics qt JOIN testagram_local.questions q ON q.id=qt.question_id JOIN testagram_local.accounts a ON a.user_id=q.author_id WHERE qt.topic_id=t.id AND `+knowledgeVisible+`)::bigint,
       EXISTS(SELECT 1 FROM testagram_local.topic_follows mine WHERE mine.topic_id=t.id AND mine.user_id=$1::uuid)
FROM testagram_local.topics t
WHERE t.status='active'
  AND ($2='' OR t.name ILIKE '%' || $2 || '%' OR t.slug ILIKE '%' || $2 || '%')
  AND ($3='' OR t.slug>$3)
ORDER BY t.slug,t.id
LIMIT $4`, uid, query, after, limit+1)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("TOPICS_UNAVAILABLE", "Topics could not be loaded", rid))
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0, limit)
	next := ""
	more := false
	for rows.Next() {
		var id, slug, name, description string
		var followers, questions int64
		var following bool
		if err := rows.Scan(&id, &slug, &name, &description, &followers, &questions, &following); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("TOPICS_UNAVAILABLE", "Topic data could not be decoded", rid))
			return
		}
		if len(items) == limit {
			more = true
			break
		}
		items = append(items, map[string]any{"id": id, "slug": slug, "name": name, "description": description, "followers_count": followers, "questions_count": questions, "is_following": following})
		next = slug
	}
	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("TOPICS_UNAVAILABLE", "Topics could not be loaded", rid))
		return
	}
	cursor := ""
	if more {
		cursor = next
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"items": items, "has_more": more, "next_cursor": cursor}, "error": nil, "request_id": rid})
}

func (s *service) knowledgeTopicRead(w http.ResponseWriter, r *http.Request) {
	uid, rid, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	slug, valid := normalizeKnowledgeTopicSlug(r.PathValue("topic_slug"))
	if !valid {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_TOPIC", "Topic slug is invalid", rid))
		return
	}
	var id, name, description string
	var followers, questions int64
	var following bool
	err := s.db.QueryRow(ctx, `
SELECT t.id::text,t.name,t.description,
       (SELECT count(*) FROM testagram_local.topic_follows tf JOIN testagram_local.accounts follower ON follower.user_id=tf.user_id WHERE tf.topic_id=t.id AND follower.account_status='active')::bigint,
       (SELECT count(*) FROM testagram_local.question_topics qt JOIN testagram_local.questions q ON q.id=qt.question_id JOIN testagram_local.accounts a ON a.user_id=q.author_id WHERE qt.topic_id=t.id AND `+knowledgeVisible+`)::bigint,
       EXISTS(SELECT 1 FROM testagram_local.topic_follows mine WHERE mine.topic_id=t.id AND mine.user_id=$1::uuid)
FROM testagram_local.topics t
WHERE t.slug=$2 AND t.status='active'`, uid, slug).Scan(&id, &name, &description, &followers, &questions, &following)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, errorBody("TOPIC_NOT_FOUND", "Topic is unavailable", rid))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("TOPIC_UNAVAILABLE", "Topic could not be loaded", rid))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"id": id, "slug": slug, "name": name, "description": description, "followers_count": followers, "questions_count": questions, "is_following": following}, "error": nil, "request_id": rid})
}

func (s *service) knowledgeTopicFollow(w http.ResponseWriter, r *http.Request) {
	uid, rid, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	action := "knowledge-topic-follow"
	if r.Method == http.MethodDelete {
		action = "knowledge-topic-unfollow"
	}
	if !s.socialWriteAllowed(w, r, uid, rid, action, 60) {
		return
	}
	slug, valid := normalizeKnowledgeTopicSlug(r.PathValue("topic_slug"))
	if !valid {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_TOPIC", "Topic slug is invalid", rid))
		return
	}
	var topicID string
	if err := s.db.QueryRow(ctx, `SELECT id::text FROM testagram_local.topics WHERE slug=$1 AND status='active'`, slug).Scan(&topicID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorBody("TOPIC_NOT_FOUND", "Topic is unavailable", rid))
		} else {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("TOPIC_UNAVAILABLE", "Topic could not be loaded", rid))
		}
		return
	}
	if r.Method == http.MethodPost {
		_, err := s.db.Exec(ctx, `INSERT INTO testagram_local.topic_follows(user_id,topic_id) VALUES($1::uuid,$2::uuid) ON CONFLICT(user_id,topic_id) DO NOTHING`, uid, topicID)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("TOPIC_FOLLOW_FAILED", "Topic could not be followed", rid))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"slug": slug, "is_following": true}, "error": nil, "request_id": rid})
		return
	}
	if r.Method == http.MethodDelete {
		if _, err := s.db.Exec(ctx, `DELETE FROM testagram_local.topic_follows WHERE user_id=$1::uuid AND topic_id=$2::uuid`, uid, topicID); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("TOPIC_UNFOLLOW_FAILED", "Topic could not be unfollowed", rid))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"slug": slug, "is_following": false}, "error": nil, "request_id": rid})
		return
	}
	writeJSON(w, http.StatusMethodNotAllowed, errorBody("METHOD_NOT_ALLOWED", "Method is not supported", rid))
}
