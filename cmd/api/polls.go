package main

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type pollCreateRequest struct {
	Question       string     `json:"question"`
	Description    string     `json:"description"`
	Options        []string   `json:"options"`
	EndsAt         *time.Time `json:"ends_at"`
	MediaObjectKey string     `json:"media_object_key"`
}

type pollOptionDTO struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Position int    `json:"position"`
	Votes    int64  `json:"votes"`
}

type pollDTO struct {
	ID             string          `json:"id"`
	Question       string          `json:"question"`
	Description    string          `json:"description"`
	Status         string          `json:"status"`
	EndsAt         *time.Time      `json:"ends_at"`
	CreatedAt      time.Time       `json:"created_at"`
	TotalVotes     int64           `json:"total_votes"`
	Options        []pollOptionDTO `json:"options"`
	HasVoted       bool            `json:"has_voted"`
	MediaURL       string          `json:"media_url,omitempty"`
	MediaType      string          `json:"media_type,omitempty"`
	MediaObjectKey string          `json:"-"`
}

func (s *service) pollLoad(ctx context.Context, pollID, viewerID string) (pollDTO, error) {
	var p pollDTO
	var status, viewerOwnerID string
	err := s.db.QueryRow(ctx, `SELECT id::text,question,description,
		CASE WHEN status='open' AND ends_at IS NOT NULL AND ends_at<=now() THEN 'closed' ELSE status END,
		ends_at,created_at,coalesce(media_object_key,''),author_id::text FROM testagram_local.polls WHERE id=$1`, pollID).Scan(
		&p.ID, &p.Question, &p.Description, &status, &p.EndsAt, &p.CreatedAt, &p.MediaObjectKey, &viewerOwnerID)
	if err != nil {
		return p, err
	}
	p.Status = status
	if p.MediaObjectKey != "" {
		var visibility, assetStatus, namespace, contentType string
		if mediaErr := s.db.QueryRow(ctx, "SELECT visibility,status,storage_namespace,content_type FROM testagram_media.assets WHERE object_key=$1 AND owner_id=$2", p.MediaObjectKey, viewerOwnerID).Scan(&visibility, &assetStatus, &namespace, &contentType); mediaErr == nil && visibility == "public" && assetStatus == "ready" {
			p.MediaURL = publicMediaURLForStorage(p.MediaObjectKey, viewerOwnerID, namespace)
			p.MediaType = contentType
		}
	}
	p.Options = []pollOptionDTO{}
	rows, err := s.db.Query(ctx, `SELECT o.id::text,o.label,o.position,count(v.user_id)::bigint
		FROM testagram_local.poll_options o
		LEFT JOIN testagram_local.poll_votes v ON v.poll_id=o.poll_id AND v.option_id=o.id
		WHERE o.poll_id=$1 GROUP BY o.id,o.label,o.position ORDER BY o.position`, pollID)
	if err != nil {
		return p, err
	}
	for rows.Next() {
		var o pollOptionDTO
		if err := rows.Scan(&o.ID, &o.Label, &o.Position, &o.Votes); err != nil {
			rows.Close()
			return p, err
		}
		p.TotalVotes += o.Votes
		p.Options = append(p.Options, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return p, err
	}
	if viewerID != "" {
		err = s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM testagram_local.poll_votes WHERE poll_id=$1 AND user_id=$2)`, pollID, viewerID).Scan(&p.HasVoted)
	}
	return p, err
}

func (s *service) socialOptionalReadAuth(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	rid := requestID(r)
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", rid))
		return "", rid, false
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("SOCIAL_DATABASE_UNAVAILABLE", "Social data service is unavailable", rid))
		return "", rid, false
	}
	if strings.TrimSpace(r.Header.Get("Authorization")) == "" {
		return "", rid, true
	}
	return s.socialAuth(w, r)
}

func (s *service) pollsList(w http.ResponseWriter, r *http.Request) {
	userID, requestID, ok := s.socialOptionalReadAuth(w, r)
	if !ok {
		return
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 50 {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_LIMIT", "Limit must be between 1 and 50", requestID))
			return
		}
		limit = n
	}
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > 10000 {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_OFFSET", "Offset is invalid", requestID))
			return
		}
		offset = n
	}
	rows, err := s.db.Query(r.Context(), `SELECT id::text FROM testagram_local.polls
		WHERE status IN ('open','closed') ORDER BY created_at DESC,id DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POLL_LIST_FAILED", "Polls could not be loaded", requestID))
		return
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			writeJSON(w, http.StatusServiceUnavailable, errorBody("POLL_LIST_FAILED", "Polls could not be loaded", requestID))
			return
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POLL_LIST_FAILED", "Polls could not be loaded", requestID))
		return
	}
	items := make([]pollDTO, 0, len(ids))
	for _, id := range ids {
		p, e := s.pollLoad(r.Context(), id, userID)
		if e != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("POLL_LIST_FAILED", "Poll details could not be loaded", requestID))
			return
		}
		items = append(items, p)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"items": items, "next_offset": offset + len(items), "has_more": len(items) == limit}, "error": nil, "request_id": requestID})
}

func (s *service) pollCreate(w http.ResponseWriter, r *http.Request) {
	userID, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	if !s.socialWriteAllowed(w, r, userID, requestID, "poll-create", 5) {
		return
	}
	var in pollCreateRequest
	if err := decodeMediaJSON(w, r, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_POLL", "Poll payload is invalid", requestID))
		return
	}
	in.Question = strings.TrimSpace(in.Question)
	in.Description = strings.TrimSpace(in.Description)
	if utf8.RuneCountInString(in.Question) < 5 || utf8.RuneCountInString(in.Question) > 500 || utf8.RuneCountInString(in.Description) > 1000 || len(in.Options) < 2 || len(in.Options) > 8 {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_POLL", "A poll needs a 5–500 character question and 2–8 options", requestID))
		return
	}
	for i := range in.Options {
		in.Options[i] = strings.TrimSpace(in.Options[i])
		if utf8.RuneCountInString(in.Options[i]) < 1 || utf8.RuneCountInString(in.Options[i]) > 200 {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_POLL_OPTION", "Each option must contain 1–200 characters", requestID))
			return
		}
	}
	if in.EndsAt != nil && !in.EndsAt.After(time.Now()) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_POLL_END_DATE", "Poll closing time must be in the future", requestID))
		return
	}
	if in.MediaObjectKey != "" {
		var visibility, assetStatus, contentType, externalMediaID string
		err := s.db.QueryRow(r.Context(), "SELECT visibility,status,content_type,coalesce(external_media_id::text,'') FROM testagram_media.assets WHERE object_key=$1 AND owner_id=$2", strings.TrimSpace(in.MediaObjectKey), userID).Scan(&visibility, &assetStatus, &contentType, &externalMediaID)
		if err != nil || visibility != "public" || assetStatus != "ready" || externalMediaID != "" || (!strings.HasPrefix(contentType, "image/") && !strings.HasPrefix(contentType, "video/")) {
			writeJSON(w, http.StatusBadRequest, errorBody("POLL_MEDIA_NOT_ATTACHABLE", "Poll media must be a ready public image or video owned by this account", requestID))
			return
		}
		in.MediaObjectKey = strings.TrimSpace(in.MediaObjectKey)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POLL_CREATE_FAILED", "Poll could not be created", requestID))
		return
	}
	defer tx.Rollback(ctx)
	var pollID string
	err = tx.QueryRow(ctx, `INSERT INTO testagram_local.polls(author_id,question,description,ends_at,media_object_key)
		VALUES($1,$2,$3,$4,NULLIF($5,'')) RETURNING id::text`, userID, in.Question, in.Description, in.EndsAt, in.MediaObjectKey).Scan(&pollID)
	if err == nil {
		for i, label := range in.Options {
			_, err = tx.Exec(ctx, `INSERT INTO testagram_local.poll_options(poll_id,label,position) VALUES($1,$2,$3)`, pollID, label, i)
			if err != nil {
				break
			}
		}
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POLL_CREATE_FAILED", "Poll could not be saved", requestID))
		return
	}
	if err = tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POLL_CREATE_FAILED", "Poll could not be committed", requestID))
		return
	}
	p, err := s.pollLoad(ctx, pollID, userID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POLL_CREATE_FAILED", "Poll was saved but could not be returned", requestID))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "data": p, "error": nil, "request_id": requestID})
}

func (s *service) pollRead(w http.ResponseWriter, r *http.Request) {
	userID, requestID, ok := s.socialOptionalReadAuth(w, r)
	if !ok {
		return
	}
	id := strings.ToLower(r.PathValue("poll_id"))
	if !socialUUIDPattern.MatchString(id) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_POLL_ID", "A valid poll ID is required", requestID))
		return
	}
	p, err := s.pollLoad(r.Context(), id, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, errorBody("POLL_NOT_FOUND", "Poll was not found", requestID))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POLL_READ_FAILED", "Poll could not be loaded", requestID))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": p, "error": nil, "request_id": requestID})
}

func (s *service) pollByPostRead(w http.ResponseWriter, r *http.Request) {
	userID, requestID, ok := s.socialOptionalReadAuth(w, r)
	if !ok {
		return
	}
	postID := strings.ToLower(r.PathValue("post_id"))
	if !socialUUIDPattern.MatchString(postID) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_POST_ID", "A valid post ID is required", requestID))
		return
	}
	var id string
	err := s.db.QueryRow(r.Context(), `SELECT id::text FROM testagram_local.polls WHERE post_id=$1`, postID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, errorBody("POLL_NOT_FOUND", "This post has no poll", requestID))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POLL_READ_FAILED", "Poll could not be loaded", requestID))
		return
	}
	p, err := s.pollLoad(r.Context(), id, userID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POLL_READ_FAILED", "Poll could not be loaded", requestID))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": p, "error": nil, "request_id": requestID})
}

func (s *service) pollVote(w http.ResponseWriter, r *http.Request) {
	userID, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	if !s.socialWriteAllowed(w, r, userID, requestID, "poll-vote", 30) {
		return
	}
	pollID := strings.ToLower(r.PathValue("poll_id"))
	if !socialUUIDPattern.MatchString(pollID) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_POLL_ID", "A valid poll ID is required", requestID))
		return
	}
	var in struct {
		OptionID string `json:"option_id"`
	}
	if err := decodeMediaJSON(w, r, &in); err != nil || !socialUUIDPattern.MatchString(in.OptionID) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_POLL_OPTION", "A valid option_id is required", requestID))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tag, err := s.db.Exec(ctx, `INSERT INTO testagram_local.poll_votes(poll_id,option_id,user_id)
		SELECT p.id,o.id,$3 FROM testagram_local.polls p JOIN testagram_local.poll_options o ON o.poll_id=p.id
		WHERE p.id=$1 AND o.id=$2 AND p.status='open' AND (p.ends_at IS NULL OR p.ends_at>now())
		ON CONFLICT(poll_id,user_id) DO NOTHING`, pollID, strings.ToLower(in.OptionID), userID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POLL_VOTE_FAILED", "Vote could not be recorded", requestID))
		return
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		_ = s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM testagram_local.poll_votes WHERE poll_id=$1 AND user_id=$2)`, pollID, userID).Scan(&exists)
		if exists {
			writeJSON(w, http.StatusConflict, errorBody("POLL_ALREADY_VOTED", "You have already voted in this poll", requestID))
			return
		}
		writeJSON(w, http.StatusConflict, errorBody("POLL_CLOSED_OR_OPTION_INVALID", "Poll is closed or the option is invalid", requestID))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"poll_id": pollID, "option_id": strings.ToLower(in.OptionID), "voted": true}, "error": nil, "request_id": requestID})
}
