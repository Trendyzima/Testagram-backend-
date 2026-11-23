package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type knowledgeQuestionInput struct {
	Title      string `json:"title"`
	Body       string `json:"body"`
	Visibility string `json:"visibility"`
	Language   string `json:"language"`
	TopicSlugs []string `json:"topic_slugs,omitempty"`
}
type knowledgeAnswerInput struct {
	Body string `json:"body"`
}

const knowledgeVisible = `
 q.deleted_at IS NULL AND q.status <> 'removed' AND a.account_status='active'
 AND (q.author_id=$1
   OR (q.visibility='public' AND NOT a.protected_account)
   OR (q.visibility IN ('public','followers') AND EXISTS (
     SELECT 1 FROM testagram_local.follows f
     WHERE f.follower_id=$1 AND f.following_id=q.author_id AND f.status='accepted')))
 AND NOT EXISTS (SELECT 1 FROM testagram_local.blocks b
   WHERE (b.blocker_id=$1 AND b.blocked_id=q.author_id)
      OR (b.blocker_id=q.author_id AND b.blocked_id=$1))
 AND NOT EXISTS (SELECT 1 FROM testagram_local.mutes m WHERE m.muter_id=$1 AND m.muted_id=q.author_id)`

func knowledgeHash(v any) (string, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return "", e
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}
func knowledgeKeyHash(v string) string {
	h := sha256.Sum256([]byte(v))
	return hex.EncodeToString(h[:])
}
func knowledgeDecode(w http.ResponseWriter, r *http.Request, dst any, limit int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(dst); e != nil {
		return e
	}
	if e := d.Decode(&struct{}{}); e != io.EOF {
		return errors.New("one JSON object required")
	}
	return nil
}
func (s *service) knowledgeQuestions(w http.ResponseWriter, r *http.Request) {
	uid, rid, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodPost {
		s.knowledgeQuestionCreate(w, r, uid, rid)
		return
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 1 || n > 50 {
			writeJSON(w, 400, errorBody("INVALID_LIMIT", "Limit must be 1-50", rid))
			return
		}
		limit = n
	}
	status := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status")))
	if status == "" {
		status = "all"
	}
	if status != "all" && status != "open" && status != "answered" {
		writeJSON(w, 400, errorBody("INVALID_STATUS", "Status must be all, open, or answered", rid))
		return
	}
	var before, beforeID any
	if raw := r.URL.Query().Get("before"); raw != "" {
		c, e := decodeSocialCursor(raw)
		if e != nil {
			writeJSON(w, 400, errorBody("INVALID_CURSOR", "Cursor is invalid", rid))
			return
		}
		before, beforeID = c.CreatedAt, c.ID
	}
	mine := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("mine")), "true")
	following := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("following")), "true")
	topicSlug := strings.TrimSpace(r.PathValue("topic_slug"))
	if topicSlug != "" {
		var valid bool
		topicSlug, valid = normalizeKnowledgeTopicSlug(topicSlug)
		if !valid {
			writeJSON(w, 400, errorBody("INVALID_TOPIC", "Topic slug is invalid", rid))
			return
		}
		var exists bool
		if err := s.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM testagram_local.topics WHERE slug=$1 AND status='active')`, topicSlug).Scan(&exists); err != nil {
			writeJSON(w, 503, errorBody("TOPIC_LOOKUP_FAILED", "Topic could not be checked", rid))
			return
		}
		if !exists {
			writeJSON(w, 404, errorBody("TOPIC_NOT_FOUND", "Topic is unavailable", rid))
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	rows, e := s.db.Query(ctx, `SELECT q.id::text,q.author_id::text,q.title,q.body,q.visibility,q.status,COALESCE(q.accepted_answer_id::text,''),q.created_at,q.updated_at,a.username,a.display_name,a.avatar_key,a.protected_account,(SELECT count(*) FROM testagram_local.answers x WHERE x.question_id=q.id AND x.deleted_at IS NULL),
COALESCE((SELECT json_agg(json_build_object('slug',t.slug,'name',t.name) ORDER BY t.name)::text FROM testagram_local.question_topics qt JOIN testagram_local.topics t ON t.id=qt.topic_id WHERE qt.question_id=q.id AND t.status='active'),'[]')
FROM testagram_local.questions q JOIN testagram_local.accounts a ON a.user_id=q.author_id
WHERE `+knowledgeVisible+` AND ($2='all' OR q.status=$2)
AND (NOT $3::boolean OR q.author_id=$1)
AND (NOT $4::boolean OR EXISTS(SELECT 1 FROM testagram_local.follows ff WHERE ff.follower_id=$1 AND ff.following_id=q.author_id AND ff.status='accepted'))
AND ($5::timestamptz IS NULL OR (q.created_at,q.id)<($5::timestamptz,$6::uuid))
AND ($8='' OR EXISTS(SELECT 1 FROM testagram_local.question_topics qtf JOIN testagram_local.topics tf ON tf.id=qtf.topic_id WHERE qtf.question_id=q.id AND tf.slug=$8 AND tf.status='active'))
ORDER BY q.created_at DESC,q.id DESC LIMIT $7`, uid, status, mine, following, before, beforeID, limit+1, topicSlug)
	if e != nil {
		writeJSON(w, 503, errorBody("QUESTIONS_UNAVAILABLE", "Questions could not be loaded", rid))
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	var next socialCursor
	more := false
	for rows.Next() {
		var id, author, title, body, vis, state, accepted, username, display, avatar string
		var created, updated time.Time
		var protected bool
		var count int64
		var topicsJSON string
		if e := rows.Scan(&id, &author, &title, &body, &vis, &state, &accepted, &created, &updated, &username, &display, &avatar, &protected, &count, &topicsJSON); e != nil {
			writeJSON(w, 503, errorBody("QUESTIONS_UNAVAILABLE", "Question data could not be decoded", rid))
			return
		}
		if len(items) == limit {
			more = true
			break
		}
		topics := []map[string]any{}
		_ = json.Unmarshal([]byte(topicsJSON), &topics)
		items = append(items, map[string]any{"id": id, "author_id": author, "title": title, "body": body, "visibility": vis, "status": state, "accepted_answer_id": accepted, "created_at": created, "updated_at": updated, "answers_count": count, "topics": topics, "author": map[string]any{"id": author, "username": username, "display_name": display, "avatar_key": avatar, "avatar_url": publicMediaURL(avatar), "protected_account": protected}})
		next = socialCursor{CreatedAt: created, ID: id}
	}
	if e := rows.Err(); e != nil {
		writeJSON(w, 503, errorBody("QUESTIONS_UNAVAILABLE", "Question data could not be loaded", rid))
		return
	}
	cursor := ""
	if more && len(items) > 0 {
		cursor = encodeSocialCursor(next)
	}
	writeJSON(w, 200, map[string]any{"ok": true, "data": map[string]any{"items": items, "has_more": more, "next_cursor": cursor}, "error": nil, "request_id": rid})
}
func (s *service) knowledgeQuestionCreate(w http.ResponseWriter, r *http.Request, uid, rid string) {
	if !s.socialWriteAllowed(w, r, uid, rid, "knowledge-question-create", 5) {
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if !validSocialPostIdempotencyKey(key) {
		writeJSON(w, 400, errorBody("IDEMPOTENCY_KEY_REQUIRED", "A safe idempotency key is required", rid))
		return
	}
	var in knowledgeQuestionInput
	if e := knowledgeDecode(w, r, &in, 24<<10); e != nil {
		writeJSON(w, 400, errorBody("INVALID_QUESTION", "Question payload is invalid", rid))
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	in.Body = strings.TrimSpace(in.Body)
	in.Visibility = strings.ToLower(strings.TrimSpace(in.Visibility))
	in.Language = strings.ToLower(strings.TrimSpace(in.Language))
	var topicsValid bool
	in.TopicSlugs, topicsValid = normalizeKnowledgeTopicSlugs(in.TopicSlugs)
	if !topicsValid {
		writeJSON(w, 400, errorBody("INVALID_QUESTION_TOPICS", "A question can have up to five valid topic slugs", rid))
		return
	}
	if !utf8.ValidString(in.Title) || utf8.RuneCountInString(in.Title) < 8 || utf8.RuneCountInString(in.Title) > 180 || !utf8.ValidString(in.Body) || utf8.RuneCountInString(in.Body) > 20000 {
		writeJSON(w, 400, errorBody("INVALID_QUESTION_CONTENT", "Title must be 8-180 characters; body at most 20,000", rid))
		return
	}
	if in.Visibility == "" {
		in.Visibility = "public"
	}
	if in.Visibility != "public" && in.Visibility != "followers" && in.Visibility != "private" {
		writeJSON(w, 400, errorBody("INVALID_VISIBILITY", "Visibility must be public, followers, or private", rid))
		return
	}
	if len(in.Language) > 32 {
		writeJSON(w, 400, errorBody("INVALID_LANGUAGE", "Language must be at most 32 characters", rid))
		return
	}
	hash, e := knowledgeHash(in)
	if e != nil {
		writeJSON(w, 400, errorBody("INVALID_QUESTION", "Question could not be normalized", rid))
		return
	}
	kh := knowledgeKeyHash(key)
	var oldHash, oldID string
	e = s.db.QueryRow(r.Context(), `SELECT request_hash,question_id::text FROM testagram_local.question_create_idempotency WHERE author_id=$1 AND key_hash=$2`, uid, kh).Scan(&oldHash, &oldID)
	if e == nil {
		if oldHash != hash {
			writeJSON(w, 409, errorBody("IDEMPOTENCY_CONFLICT", "Key was used for different content", rid))
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "data": map[string]any{"id": oldID, "idempotent_replay": true}, "error": nil, "request_id": rid})
		return
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		writeJSON(w, 503, errorBody("QUESTION_LOOKUP_FAILED", "Retry status is unavailable", rid))
		return
	}
	if s.moderation == nil {
		writeJSON(w, 503, errorBody("QUESTION_MODERATION_UNAVAILABLE", "Moderation is not configured; no question was published", rid))
		return
	}
	mctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	mr, me := s.moderation.Analyze(mctx, in.Title+"\n"+in.Body)
	cancel()
	if me != nil {
		writeJSON(w, 503, errorBody("QUESTION_MODERATION_UNAVAILABLE", "Moderation unavailable; no question was published", rid))
		return
	}
	if mr.Action != "pass" {
		writeJSON(w, 422, map[string]any{"ok": false, "data": mr, "error": map[string]string{"code": "CONTENT_REQUIRES_REVIEW", "message": mr.Reason}, "request_id": rid})
		return
	}
	tx, e := s.db.Begin(r.Context())
	if e != nil {
		writeJSON(w, 503, errorBody("QUESTION_CREATE_FAILED", "Could not start question", rid))
		return
	}
	defer tx.Rollback(context.Background())
	if _, e = tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", uid+":"+kh); e != nil {
		writeJSON(w, 503, errorBody("QUESTION_CREATE_FAILED", "Could not lock retry", rid))
		return
	}
	e = tx.QueryRow(r.Context(), `SELECT request_hash,question_id::text FROM testagram_local.question_create_idempotency WHERE author_id=$1 AND key_hash=$2`, uid, kh).Scan(&oldHash, &oldID)
	if e == nil {
		if oldHash != hash {
			writeJSON(w, 409, errorBody("IDEMPOTENCY_CONFLICT", "Key was used for different content", rid))
			return
		}
		if e = tx.Commit(r.Context()); e != nil {
			writeJSON(w, 503, errorBody("QUESTION_CREATE_FAILED", "Could not confirm retry", rid))
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "data": map[string]any{"id": oldID, "idempotent_replay": true}, "error": nil, "request_id": rid})
		return
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		writeJSON(w, 503, errorBody("QUESTION_LOOKUP_FAILED", "Retry status is unavailable", rid))
		return
	}
	var id string
	e = tx.QueryRow(r.Context(), `INSERT INTO testagram_local.questions(author_id,title,body,visibility,language) VALUES($1,$2,$3,$4,$5) RETURNING id::text`, uid, in.Title, in.Body, in.Visibility, in.Language).Scan(&id)
	if e == nil && len(in.TopicSlugs) > 0 {
		tag, topicErr := tx.Exec(r.Context(), `INSERT INTO testagram_local.question_topics(question_id,topic_id,added_by)
SELECT $1::uuid,t.id,$2::uuid
FROM unnest($3::text[]) AS requested(slug)
JOIN testagram_local.topics t ON t.slug=requested.slug AND t.status='active'`, id, uid, in.TopicSlugs)
		if topicErr != nil {
			e = topicErr
		} else if tag.RowsAffected() != int64(len(in.TopicSlugs)) {
			writeJSON(w, 400, errorBody("INVALID_QUESTION_TOPICS", "One or more topics are unavailable", rid))
			return
		}
	}
	if e == nil {
		_, e = tx.Exec(r.Context(), `INSERT INTO testagram_local.question_create_idempotency(author_id,key_hash,request_hash,question_id) VALUES($1,$2,$3,$4)`, uid, kh, hash, id)
	}
	if e != nil {
		writeJSON(w, 503, errorBody("QUESTION_CREATE_FAILED", "Could not save question", rid))
		return
	}
	if e = tx.Commit(r.Context()); e != nil {
		writeJSON(w, 503, errorBody("QUESTION_CREATE_FAILED", "Could not commit question", rid))
		return
	}
	writeJSON(w, 201, map[string]any{"ok": true, "data": map[string]any{"id": id, "author_id": uid, "title": in.Title, "body": in.Body, "visibility": in.Visibility, "status": "open", "topic_slugs": in.TopicSlugs}, "error": nil, "request_id": rid})
}
func (s *service) knowledgeQuestionRead(w http.ResponseWriter, r *http.Request) {
	uid, rid, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	id := strings.ToLower(strings.TrimSpace(r.PathValue("question_id")))
	if !socialUUIDPattern.MatchString(id) {
		writeJSON(w, 400, errorBody("INVALID_QUESTION_ID", "Question ID is invalid", rid))
		return
	}
	var qid, author, title, body, vis, state, accepted, username, display, avatar string
	var created, updated time.Time
	var protected bool
	e := s.db.QueryRow(r.Context(), `SELECT q.id::text,q.author_id::text,q.title,q.body,q.visibility,q.status,COALESCE(q.accepted_answer_id::text,''),q.created_at,q.updated_at,a.username,a.display_name,a.avatar_key,a.protected_account FROM testagram_local.questions q JOIN testagram_local.accounts a ON a.user_id=q.author_id WHERE q.id=$2 AND `+knowledgeVisible, uid, id).Scan(&qid, &author, &title, &body, &vis, &state, &accepted, &created, &updated, &username, &display, &avatar, &protected)
	if errors.Is(e, pgx.ErrNoRows) {
		writeJSON(w, 404, errorBody("QUESTION_NOT_FOUND", "Question not found", rid))
		return
	}
	if e != nil {
		writeJSON(w, 503, errorBody("QUESTION_READ_FAILED", "Question could not be loaded", rid))
		return
	}
	rows, e := s.db.Query(r.Context(), `SELECT an.id::text,an.question_id::text,an.author_id::text,an.body,an.revision_number,an.created_at,an.updated_at,(an.id=q.accepted_answer_id),aa.username,aa.display_name,aa.avatar_key FROM testagram_local.answers an JOIN testagram_local.questions q ON q.id=an.question_id JOIN testagram_local.accounts aa ON aa.user_id=an.author_id WHERE an.question_id=$2 AND an.deleted_at IS NULL AND aa.account_status='active' AND NOT EXISTS(SELECT 1 FROM testagram_local.blocks b WHERE (b.blocker_id=$1 AND b.blocked_id=an.author_id) OR (b.blocker_id=an.author_id AND b.blocked_id=$1)) AND NOT EXISTS(SELECT 1 FROM testagram_local.mutes m WHERE m.muter_id=$1 AND m.muted_id=an.author_id) ORDER BY (an.id=q.accepted_answer_id) DESC,an.created_at,an.id`, uid, id)
	if e != nil {
		writeJSON(w, 503, errorBody("ANSWERS_UNAVAILABLE", "Answers could not be loaded", rid))
		return
	}
	defer rows.Close()
	answers := []map[string]any{}
	for rows.Next() {
		var aid, qid2, au, ab, ak, av, txt string
		var rev int
		var ac bool
		var c, u time.Time
		if e := rows.Scan(&aid, &qid2, &au, &txt, &rev, &c, &u, &ac, &ab, &ak, &av); e != nil {
			writeJSON(w, 503, errorBody("ANSWERS_UNAVAILABLE", "Answer data could not be decoded", rid))
			return
		}
		answers = append(answers, map[string]any{"id": aid, "question_id": qid2, "author_id": au, "body": txt, "revision_number": rev, "created_at": c, "updated_at": u, "accepted": ac, "author": map[string]any{"id": au, "username": ab, "display_name": ak, "avatar_key": av, "avatar_url": publicMediaURL(av)}})
	}
	if e := rows.Err(); e != nil {
		writeJSON(w, 503, errorBody("ANSWERS_UNAVAILABLE", "Answer data could not be loaded", rid))
		return
	}
	rows.Close()
	topicRows, topicErr := s.db.Query(r.Context(), `SELECT t.slug,t.name FROM testagram_local.question_topics qt JOIN testagram_local.topics t ON t.id=qt.topic_id WHERE qt.question_id=$1::uuid AND t.status='active' ORDER BY t.name,t.slug`, qid)
	if topicErr != nil {
		writeJSON(w, 503, errorBody("QUESTION_TOPICS_UNAVAILABLE", "Question topics could not be loaded", rid))
		return
	}
	topics := []map[string]string{}
	for topicRows.Next() {
		var slug, name string
		if err := topicRows.Scan(&slug, &name); err != nil {
			topicRows.Close()
			writeJSON(w, 503, errorBody("QUESTION_TOPICS_UNAVAILABLE", "Question topics could not be decoded", rid))
			return
		}
		topics = append(topics, map[string]string{"slug": slug, "name": name})
	}
	if err := topicRows.Err(); err != nil {
		topicRows.Close()
		writeJSON(w, 503, errorBody("QUESTION_TOPICS_UNAVAILABLE", "Question topics could not be loaded", rid))
		return
	}
	topicRows.Close()
	data := map[string]any{"id": qid, "author_id": author, "title": title, "body": body, "visibility": vis, "status": state, "accepted_answer_id": accepted, "topics": topics, "created_at": created, "updated_at": updated, "author": map[string]any{"id": author, "username": username, "display_name": display, "avatar_key": avatar, "avatar_url": publicMediaURL(avatar), "protected_account": protected}, "answers": answers, "can_accept_answer": author == uid}
	writeJSON(w, 200, map[string]any{"ok": true, "data": data, "error": nil, "request_id": rid})
}
func (s *service) knowledgeAnswerCreate(w http.ResponseWriter, r *http.Request) {
	uid, rid, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	if !s.socialWriteAllowed(w, r, uid, rid, "knowledge-answer-create", 10) {
		return
	}
	qid := strings.ToLower(strings.TrimSpace(r.PathValue("question_id")))
	if !socialUUIDPattern.MatchString(qid) {
		writeJSON(w, 400, errorBody("INVALID_QUESTION_ID", "Question ID is invalid", rid))
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if !validSocialPostIdempotencyKey(key) {
		writeJSON(w, 400, errorBody("IDEMPOTENCY_KEY_REQUIRED", "A safe idempotency key is required", rid))
		return
	}
	var in knowledgeAnswerInput
	if e := knowledgeDecode(w, r, &in, 24<<10); e != nil {
		writeJSON(w, 400, errorBody("INVALID_ANSWER", "Answer payload is invalid", rid))
		return
	}
	in.Body = strings.TrimSpace(in.Body)
	if !utf8.ValidString(in.Body) || utf8.RuneCountInString(in.Body) < 1 || utf8.RuneCountInString(in.Body) > 20000 {
		writeJSON(w, 400, errorBody("INVALID_ANSWER_CONTENT", "Answer must contain 1-20,000 valid characters", rid))
		return
	}
	hash, e := knowledgeHash(struct{ QuestionID, Body string }{qid, in.Body})
	if e != nil {
		writeJSON(w, 400, errorBody("INVALID_ANSWER", "Answer could not be normalized", rid))
		return
	}
	kh := knowledgeKeyHash(key)
	var oldHash, oldID string
	e = s.db.QueryRow(r.Context(), `SELECT request_hash,answer_id::text FROM testagram_local.answer_create_idempotency WHERE author_id=$1 AND key_hash=$2`, uid, kh).Scan(&oldHash, &oldID)
	if e == nil {
		if oldHash != hash {
			writeJSON(w, 409, errorBody("IDEMPOTENCY_CONFLICT", "Key was used for different answer content", rid))
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "data": map[string]any{"id": oldID, "idempotent_replay": true}, "error": nil, "request_id": rid})
		return
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		writeJSON(w, 503, errorBody("ANSWER_LOOKUP_FAILED", "Retry status is unavailable", rid))
		return
	}
	var state string
	e = s.db.QueryRow(r.Context(), `SELECT q.status FROM testagram_local.questions q JOIN testagram_local.accounts a ON a.user_id=q.author_id WHERE q.id=$2 AND `+knowledgeVisible, uid, qid).Scan(&state)
	if errors.Is(e, pgx.ErrNoRows) {
		writeJSON(w, 404, errorBody("QUESTION_NOT_FOUND", "Question not found", rid))
		return
	}
	if e != nil {
		writeJSON(w, 503, errorBody("QUESTION_READ_FAILED", "Question could not be checked", rid))
		return
	}
	if state != "open" {
		writeJSON(w, 409, errorBody("QUESTION_CLOSED", "Question is not accepting answers", rid))
		return
	}
	if s.moderation == nil {
		writeJSON(w, 503, errorBody("ANSWER_MODERATION_UNAVAILABLE", "Moderation is not configured; no answer was published", rid))
		return
	}
	mctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	mr, me := s.moderation.Analyze(mctx, in.Body)
	cancel()
	if me != nil {
		writeJSON(w, 503, errorBody("ANSWER_MODERATION_UNAVAILABLE", "Moderation unavailable; no answer was published", rid))
		return
	}
	if mr.Action != "pass" {
		writeJSON(w, 422, map[string]any{"ok": false, "data": mr, "error": map[string]string{"code": "CONTENT_REQUIRES_REVIEW", "message": mr.Reason}, "request_id": rid})
		return
	}
	tx, e := s.db.Begin(r.Context())
	if e != nil {
		writeJSON(w, 503, errorBody("ANSWER_CREATE_FAILED", "Could not start answer", rid))
		return
	}
	defer tx.Rollback(context.Background())
	if _, e = tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", uid+":"+kh); e != nil {
		writeJSON(w, 503, errorBody("ANSWER_CREATE_FAILED", "Could not lock retry", rid))
		return
	}
	e = tx.QueryRow(r.Context(), `SELECT request_hash,answer_id::text FROM testagram_local.answer_create_idempotency WHERE author_id=$1 AND key_hash=$2`, uid, kh).Scan(&oldHash, &oldID)
	if e == nil {
		if oldHash != hash {
			writeJSON(w, 409, errorBody("IDEMPOTENCY_CONFLICT", "Key was used for different answer content", rid))
			return
		}
		if e = tx.Commit(r.Context()); e != nil {
			writeJSON(w, 503, errorBody("ANSWER_CREATE_FAILED", "Could not confirm retry", rid))
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "data": map[string]any{"id": oldID, "idempotent_replay": true}, "error": nil, "request_id": rid})
		return
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		writeJSON(w, 503, errorBody("ANSWER_LOOKUP_FAILED", "Retry status is unavailable", rid))
		return
	}
	var aid string
	e = tx.QueryRow(r.Context(), `INSERT INTO testagram_local.answers(question_id,author_id,body) SELECT q.id,$2,$3 FROM testagram_local.questions q WHERE q.id=$1 AND q.status='open' AND q.deleted_at IS NULL RETURNING id::text`, qid, uid, in.Body).Scan(&aid)
	if errors.Is(e, pgx.ErrNoRows) {
		writeJSON(w, 409, errorBody("QUESTION_CLOSED", "Question is not accepting answers", rid))
		return
	}
	if e == nil {
		_, e = tx.Exec(r.Context(), `INSERT INTO testagram_local.answer_create_idempotency(author_id,key_hash,request_hash,answer_id) VALUES($1,$2,$3,$4)`, uid, kh, hash, aid)
	}
	if e != nil {
		writeJSON(w, 503, errorBody("ANSWER_CREATE_FAILED", "Could not save answer", rid))
		return
	}
	if e = tx.Commit(r.Context()); e != nil {
		writeJSON(w, 503, errorBody("ANSWER_CREATE_FAILED", "Could not commit answer", rid))
		return
	}
	writeJSON(w, 201, map[string]any{"ok": true, "data": map[string]any{"id": aid, "question_id": qid, "author_id": uid, "body": in.Body, "revision_number": 1}, "error": nil, "request_id": rid})
}
func (s *service) knowledgeAnswerAccept(w http.ResponseWriter, r *http.Request) {
	uid, rid, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	if !s.socialWriteAllowed(w, r, uid, rid, "knowledge-answer-accept", 10) {
		return
	}
	qid := strings.ToLower(strings.TrimSpace(r.PathValue("question_id")))
	aid := strings.ToLower(strings.TrimSpace(r.PathValue("answer_id")))
	if !socialUUIDPattern.MatchString(qid) || !socialUUIDPattern.MatchString(aid) {
		writeJSON(w, 400, errorBody("INVALID_ACCEPTED_ANSWER", "Question and answer IDs are required", rid))
		return
	}
	tx, e := s.db.Begin(r.Context())
	if e != nil {
		writeJSON(w, 503, errorBody("ANSWER_ACCEPT_FAILED", "Could not start acceptance", rid))
		return
	}
	defer tx.Rollback(context.Background())
	var owner string
	e = tx.QueryRow(r.Context(), `SELECT author_id::text FROM testagram_local.questions WHERE id=$1 AND deleted_at IS NULL AND status IN ('open','answered','closed') FOR UPDATE`, qid).Scan(&owner)
	if errors.Is(e, pgx.ErrNoRows) {
		writeJSON(w, 404, errorBody("QUESTION_NOT_FOUND", "Question not found", rid))
		return
	}
	if e != nil {
		writeJSON(w, 503, errorBody("ANSWER_ACCEPT_FAILED", "Could not lock question", rid))
		return
	}
	if owner != uid {
		writeJSON(w, 403, errorBody("QUESTION_OWNER_REQUIRED", "Only the question author can accept an answer", rid))
		return
	}
	tag, e := tx.Exec(r.Context(), `UPDATE testagram_local.questions q SET accepted_answer_id=a.id,status='answered',updated_at=now(),version=version+1 FROM testagram_local.answers a WHERE q.id=$1 AND a.id=$2 AND a.question_id=q.id AND a.deleted_at IS NULL AND a.author_id<>q.author_id`, qid, aid)
	if e != nil || tag.RowsAffected() != 1 {
		writeJSON(w, 409, errorBody("ANSWER_NOT_ACCEPTABLE", "Answer must be live, belong to this question and be written by another account", rid))
		return
	}
	if e = tx.Commit(r.Context()); e != nil {
		writeJSON(w, 503, errorBody("ANSWER_ACCEPT_FAILED", "Could not commit accepted answer", rid))
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "data": map[string]any{"question_id": qid, "accepted_answer_id": aid, "status": "answered"}, "error": nil, "request_id": rid})
}
