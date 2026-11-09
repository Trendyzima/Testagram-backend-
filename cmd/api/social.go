package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const socialInteractionSummarySQL = "SELECT p.likes_count,p.replies_count,p.reposts_count," +
	"(SELECT count(*) FROM testagram_local.bookmarks b WHERE b.post_id=p.id)," +
	"COALESCE((SELECT r.emoji FROM testagram_local.post_reactions r WHERE r.post_id=p.id AND r.user_id=$2),'')," +
	"EXISTS(SELECT 1 FROM testagram_local.reposts r WHERE r.post_id=p.id AND r.user_id=$2)," +
	"EXISTS(SELECT 1 FROM testagram_local.bookmarks b WHERE b.post_id=p.id AND b.user_id=$2)," +
	"COALESCE((SELECT jsonb_object_agg(grouped.emoji,grouped.reaction_count) " +
	"FROM (SELECT emoji,count(*)::bigint AS reaction_count FROM testagram_local.post_reactions " +
	"WHERE post_id=p.id GROUP BY emoji) grouped),'{}'::jsonb) " +
	"FROM testagram_local.posts p WHERE p.id=$1"

var socialUUIDPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type socialFollowRequest struct {
	TargetUserID string `json:"target_user_id"`
}

type socialReactionRequest struct {
	Emoji string `json:"emoji"`
}

type socialReplyRequest struct {
	Content       string  `json:"content"`
	ParentReplyID *string `json:"parent_reply_id,omitempty"`
}

type socialRepostRequest struct {
	Quote string `json:"quote"`
}

type socialCursor struct {
	CreatedAt time.Time `json:"created_at"`
	ID        string    `json:"id"`
}

func encodeSocialCursor(cursor socialCursor) string {
	raw, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeSocialCursor(value string) (socialCursor, error) {
	var cursor socialCursor
	if value == "" || len(value) > 512 {
		return cursor, errors.New("invalid cursor")
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || json.Unmarshal(raw, &cursor) != nil || cursor.CreatedAt.IsZero() || !socialUUIDPattern.MatchString(cursor.ID) {
		return socialCursor{}, errors.New("invalid cursor")
	}
	return cursor, nil
}

func (s *service) socialAuth(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	requestID := requestID(r)
	if !s.cors(w, r) {
		writeJSON(w, http.StatusForbidden, errorBody("ORIGIN_NOT_ALLOWED", "Origin is not allowed", requestID))
		return "", requestID, false
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("SOCIAL_DATABASE_UNAVAILABLE", "Social data service is unavailable", requestID))
		return "", requestID, false
	}
	userID, status, err := s.authenticatedMediaUser(r)
	if err != nil {
		code, message := "AUTH_REQUIRED", "A valid authenticated session is required"
		if status >= 500 {
			code, message = "AUTH_BACKEND_UNAVAILABLE", "Authentication service is temporarily unavailable"
		}
		writeJSON(w, status, errorBody(code, message, requestID))
		return "", requestID, false
	}
	if !socialUUIDPattern.MatchString(userID) {
		writeJSON(w, http.StatusUnauthorized, errorBody("INVALID_CANONICAL_IDENTITY", "The authenticated identity is not a valid Testagram account", requestID))
		return "", requestID, false
	}
	var accountStatus string
	err = s.db.QueryRow(r.Context(), "SELECT account_status FROM testagram_local.accounts WHERE user_id=$1", strings.ToLower(userID)).Scan(&accountStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusConflict, errorBody("ACCOUNT_NOT_MIGRATED", "This account has not been migrated to the independent Testagram database", requestID))
		return "", requestID, false
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("ACCOUNT_LOOKUP_FAILED", "Canonical account verification is unavailable", requestID))
		return "", requestID, false
	}
	if accountStatus != "active" {
		writeJSON(w, http.StatusForbidden, errorBody("ACCOUNT_NOT_ACTIVE", "This account cannot perform social actions", requestID))
		return "", requestID, false
	}
	return strings.ToLower(userID), requestID, true
}

func (s *service) socialRateLimit(ctx context.Context, userID, action string, maxAttempts int) (bool, error) {
	digest := sha256.Sum256([]byte(action + ":" + userID))
	var attempts int
	err := s.db.QueryRow(ctx, `INSERT INTO testagram_auth.rate_limits(action,bucket_hash,window_start,attempts)
		VALUES($1,$2,date_trunc('minute',now()),1)
		ON CONFLICT(action,bucket_hash,window_start) DO UPDATE
		SET attempts=testagram_auth.rate_limits.attempts+1
		RETURNING attempts`, action, hex.EncodeToString(digest[:])).Scan(&attempts)
	if err != nil {
		return false, err
	}
	if attempts == 1 {
		_, _ = s.db.Exec(ctx, "DELETE FROM testagram_auth.rate_limits WHERE action=$1 AND window_start < now() - interval '1 day'", action)
	}
	return attempts <= maxAttempts, nil
}

func (s *service) socialWriteAllowed(w http.ResponseWriter, r *http.Request, userID, requestID, action string, maxAttempts int) bool {
	allowed, err := s.socialRateLimit(r.Context(), userID, action, maxAttempts)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("SOCIAL_RATE_LIMIT_UNAVAILABLE", "Social abuse protection is temporarily unavailable", requestID))
		return false
	}
	if !allowed {
		writeJSON(w, http.StatusTooManyRequests, errorBody("SOCIAL_RATE_LIMITED", "Too many actions; wait a minute and try again", requestID))
		return false
	}
	return true
}

type socialQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func socialCanViewPostWith(ctx context.Context, queryer socialQueryer, viewerID, postID string) (string, error) {
	return socialCanViewPostWithLock(ctx, queryer, viewerID, postID, "UPDATE")
}

// Read handlers use shared row locks so concurrent readers do not serialize on
// an author's account row. These locks still conflict with follow/block/privacy
// mutations, keeping the authorization decision consistent with those writes.
func socialCanViewPostReadWith(ctx context.Context, queryer socialQueryer, viewerID, postID string) (string, error) {
	return socialCanViewPostWithLock(ctx, queryer, viewerID, postID, "SHARE")
}

func socialCanViewPostWithLock(ctx context.Context, queryer socialQueryer, viewerID, postID, lockMode string) (string, error) {
	if lockMode != "UPDATE" && lockMode != "SHARE" {
		return "", errors.New("unsupported social account lock mode")
	}
	var authorID string
	err := queryer.QueryRow(ctx, "SELECT author_id::text FROM testagram_local.posts WHERE id=$1 AND deleted_at IS NULL", postID).Scan(&authorID)
	if err != nil {
		return "", err
	}
	// Follow/block mutations lock the same two account rows in UUID order.
	// Locking them here makes privacy and block checks serializable with those
	// changes for every post interaction.
	locked, err := queryer.Query(ctx, "SELECT user_id,account_status FROM testagram_local.accounts WHERE user_id IN ($1,$2) ORDER BY user_id FOR "+lockMode, viewerID, authorID)
	if err != nil {
		return "", err
	}
	lockCount := 0
	viewerActive := false
	for locked.Next() {
		var lockedID, accountStatus string
		if err := locked.Scan(&lockedID, &accountStatus); err != nil {
			locked.Close()
			return "", err
		}
		if lockedID == viewerID && accountStatus == "active" {
			viewerActive = true
		}
		lockCount++
	}
	lockErr := locked.Err()
	locked.Close()
	if lockErr != nil {
		return "", lockErr
	}
	wantLocks := 2
	if viewerID == authorID {
		wantLocks = 1
	}
	if lockCount != wantLocks || !viewerActive {
		return "", pgx.ErrNoRows
	}
	err = queryer.QueryRow(ctx, `SELECT p.author_id::text
		FROM testagram_local.posts p
		JOIN testagram_local.accounts a ON a.user_id=p.author_id
		WHERE p.id=$1 AND p.deleted_at IS NULL AND a.account_status='active'
		  AND (p.author_id=$2
		       OR (NOT a.protected_account AND p.visibility='public')
		       OR ((a.protected_account OR p.visibility='followers') AND EXISTS (
		         SELECT 1 FROM testagram_local.follows f
		         WHERE f.follower_id=$2 AND f.following_id=p.author_id AND f.status='accepted'
		       )))
		  AND NOT EXISTS (
		    SELECT 1 FROM testagram_local.blocks b
		    WHERE (b.blocker_id=p.author_id AND b.blocked_id=$2)
		       OR (b.blocker_id=$2 AND b.blocked_id=p.author_id)
		  )`, postID, viewerID).Scan(&authorID)
	return authorID, err
}

func (s *service) socialFollow(w http.ResponseWriter, r *http.Request) {
	userID, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	if !s.socialWriteAllowed(w, r, userID, requestID, "social-follow", 30) {
		return
	}
	var input socialFollowRequest
	if err := decodeMediaJSON(w, r, &input); err != nil || !socialUUIDPattern.MatchString(input.TargetUserID) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_FOLLOW_TARGET", "A valid target_user_id is required", requestID))
		return
	}
	targetID := strings.ToLower(input.TargetUserID)
	if targetID == userID {
		writeJSON(w, http.StatusBadRequest, errorBody("SELF_FOLLOW_NOT_ALLOWED", "You cannot follow your own account", requestID))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("SOCIAL_WRITE_FAILED", "Follow could not be saved", requestID))
		return
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, "SELECT user_id FROM testagram_local.accounts WHERE user_id IN ($1,$2) ORDER BY user_id FOR UPDATE", userID, targetID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("SOCIAL_WRITE_FAILED", "Follow could not be saved", requestID))
		return
	}
	foundAccounts := 0
	for rows.Next() {
		var ignored string
		if err := rows.Scan(&ignored); err != nil {
			rows.Close()
			writeJSON(w, http.StatusServiceUnavailable, errorBody("SOCIAL_WRITE_FAILED", "Follow could not be saved", requestID))
			return
		}
		foundAccounts++
	}
	rowsErr := rows.Err()
	rows.Close()
	if rowsErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("SOCIAL_WRITE_FAILED", "Follow could not be saved", requestID))
		return
	}
	if foundAccounts != 2 {
		writeJSON(w, http.StatusNotFound, errorBody("FOLLOW_TARGET_NOT_FOUND", "The account to follow does not exist", requestID))
		return
	}
	var requesterStatus string
	if err := tx.QueryRow(ctx, "SELECT account_status FROM testagram_local.accounts WHERE user_id=$1", userID).Scan(&requesterStatus); err != nil || requesterStatus != "active" {
		writeJSON(w, http.StatusForbidden, errorBody("ACCOUNT_NOT_ACTIVE", "This account cannot perform social actions", requestID))
		return
	}
	var targetStatus string
	var protected bool
	if err := tx.QueryRow(ctx, "SELECT account_status,protected_account FROM testagram_local.accounts WHERE user_id=$1", targetID).Scan(&targetStatus, &protected); err != nil || targetStatus != "active" {
		writeJSON(w, http.StatusNotFound, errorBody("FOLLOW_TARGET_NOT_FOUND", "The account to follow is unavailable", requestID))
		return
	}
	var blocked bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM testagram_local.blocks
		WHERE (blocker_id=$1 AND blocked_id=$2) OR (blocker_id=$2 AND blocked_id=$1))`, userID, targetID).Scan(&blocked); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("SOCIAL_WRITE_FAILED", "Follow could not be saved", requestID))
		return
	}
	if blocked {
		writeJSON(w, http.StatusForbidden, errorBody("FOLLOW_BLOCKED", "A block prevents this follow relationship", requestID))
		return
	}
	desired := "accepted"
	if protected {
		desired = "pending"
	}
	var previous string
	err = tx.QueryRow(ctx, "SELECT status FROM testagram_local.follows WHERE follower_id=$1 AND following_id=$2 FOR UPDATE", userID, targetID).Scan(&previous)
	if err == nil && previous == "blocked" {
		writeJSON(w, http.StatusForbidden, errorBody("FOLLOW_BLOCKED", "A block prevents this follow relationship", requestID))
		return
	}
	if err == nil && previous == desired {
		if err := tx.Commit(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("SOCIAL_WRITE_FAILED", "Follow could not be confirmed", requestID))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"status": desired, "already_following": true}, "error": nil, "request_id": requestID})
		return
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("SOCIAL_WRITE_FAILED", "Follow could not be saved", requestID))
		return
	}
	var createdAt time.Time
	_, err = tx.Exec(ctx, `INSERT INTO testagram_local.follows(follower_id,following_id,status)
		VALUES($1,$2,$3)
		ON CONFLICT(follower_id,following_id) DO UPDATE SET status=EXCLUDED.status,updated_at=now()`, userID, targetID, desired)
	if err == nil {
		err = tx.QueryRow(ctx, "SELECT created_at FROM testagram_local.follows WHERE follower_id=$1 AND following_id=$2", userID, targetID).Scan(&createdAt)
	}
	if err == nil {
		kind := "follow"
		if desired == "pending" {
			kind = "follow_request"
		}
		eventKey := fmt.Sprintf("follow:%s:%s:%d", userID, targetID, createdAt.UnixNano())
		_, err = tx.Exec(ctx, `INSERT INTO testagram_local.notifications(recipient_id,actor_id,kind,event_key)
			VALUES($1,$2,$3,$4) ON CONFLICT(recipient_id,event_key) WHERE event_key IS NOT NULL
			DO UPDATE SET kind=EXCLUDED.kind,actor_id=EXCLUDED.actor_id,created_at=now()`, targetID, userID, kind, eventKey)
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("SOCIAL_WRITE_FAILED", "Follow could not be saved", requestID))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("SOCIAL_WRITE_FAILED", "Follow could not be committed", requestID))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"status": desired, "already_following": false}, "error": nil, "request_id": requestID})
}

func (s *service) socialUnfollow(w http.ResponseWriter, r *http.Request) {
	userID, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	if !s.socialWriteAllowed(w, r, userID, requestID, "social-follow", 30) {
		return
	}
	targetID := strings.ToLower(r.PathValue("target_user_id"))
	if !socialUUIDPattern.MatchString(targetID) || targetID == userID {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_FOLLOW_TARGET", "A valid target account is required", requestID))
		return
	}
	tag, err := s.db.Exec(r.Context(), "DELETE FROM testagram_local.follows WHERE follower_id=$1 AND following_id=$2", userID, targetID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("SOCIAL_WRITE_FAILED", "Follow could not be removed", requestID))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"unfollowed": tag.RowsAffected() > 0}, "error": nil, "request_id": requestID})
}

func (s *service) socialBlock(w http.ResponseWriter, r *http.Request) {
	userID, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	if !s.socialWriteAllowed(w, r, userID, requestID, "social-block", 20) {
		return
	}
	var input socialFollowRequest
	if err := decodeMediaJSON(w, r, &input); err != nil || !socialUUIDPattern.MatchString(input.TargetUserID) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_BLOCK_TARGET", "A valid target_user_id is required", requestID))
		return
	}
	targetID := strings.ToLower(input.TargetUserID)
	if targetID == userID {
		writeJSON(w, http.StatusBadRequest, errorBody("SELF_BLOCK_NOT_ALLOWED", "You cannot block your own account", requestID))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("SOCIAL_WRITE_FAILED", "Block could not be saved", requestID))
		return
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, "SELECT user_id FROM testagram_local.accounts WHERE user_id IN ($1,$2) ORDER BY user_id FOR UPDATE", userID, targetID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("SOCIAL_WRITE_FAILED", "Block could not be saved", requestID))
		return
	}
	found := 0
	for rows.Next() {
		var ignored string
		if err := rows.Scan(&ignored); err != nil {
			rows.Close()
			writeJSON(w, http.StatusServiceUnavailable, errorBody("SOCIAL_WRITE_FAILED", "Block could not be saved", requestID))
			return
		}
		found++
	}
	rowsErr := rows.Err()
	rows.Close()
	if rowsErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("SOCIAL_WRITE_FAILED", "Block could not be saved", requestID))
		return
	}
	if found != 2 {
		writeJSON(w, http.StatusNotFound, errorBody("BLOCK_TARGET_NOT_FOUND", "The account to block does not exist", requestID))
		return
	}
	var blockerStatus string
	if err := tx.QueryRow(ctx, "SELECT account_status FROM testagram_local.accounts WHERE user_id=$1", userID).Scan(&blockerStatus); err != nil || blockerStatus != "active" {
		writeJSON(w, http.StatusForbidden, errorBody("ACCOUNT_NOT_ACTIVE", "This account cannot perform social actions", requestID))
		return
	}
	_, err = tx.Exec(ctx, "INSERT INTO testagram_local.blocks(blocker_id,blocked_id) VALUES($1,$2) ON CONFLICT DO NOTHING", userID, targetID)
	if err == nil {
		_, err = tx.Exec(ctx, "DELETE FROM testagram_local.follows WHERE (follower_id=$1 AND following_id=$2) OR (follower_id=$2 AND following_id=$1)", userID, targetID)
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("SOCIAL_WRITE_FAILED", "Block could not be saved", requestID))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("SOCIAL_WRITE_FAILED", "Block could not be committed", requestID))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"blocked": true}, "error": nil, "request_id": requestID})
}

func (s *service) socialUnblock(w http.ResponseWriter, r *http.Request) {
	userID, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	if !s.socialWriteAllowed(w, r, userID, requestID, "social-block", 20) {
		return
	}
	targetID := strings.ToLower(r.PathValue("target_user_id"))
	if !socialUUIDPattern.MatchString(targetID) || targetID == userID {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_BLOCK_TARGET", "A valid target account is required", requestID))
		return
	}
	tag, err := s.db.Exec(r.Context(), "DELETE FROM testagram_local.blocks WHERE blocker_id=$1 AND blocked_id=$2", userID, targetID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("SOCIAL_WRITE_FAILED", "Block could not be removed", requestID))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"unblocked": tag.RowsAffected() > 0}, "error": nil, "request_id": requestID})
}

func (s *service) socialReact(w http.ResponseWriter, r *http.Request) {
	userID, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	if !s.socialWriteAllowed(w, r, userID, requestID, "social-reaction", 60) {
		return
	}
	postID := strings.ToLower(r.PathValue("post_id"))
	if !socialUUIDPattern.MatchString(postID) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_POST_ID", "A valid post ID is required", requestID))
		return
	}
	var input socialReactionRequest
	if err := decodeMediaJSON(w, r, &input); err != nil || strings.TrimSpace(input.Emoji) == "" || len([]rune(input.Emoji)) > 8 {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_REACTION", "A valid reaction emoji is required", requestID))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("REACTION_WRITE_FAILED", "Reaction could not be saved", requestID))
		return
	}
	defer tx.Rollback(ctx)
	authorID, err := socialCanViewPostWith(ctx, tx, userID, postID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, errorBody("POST_NOT_FOUND", "The post is unavailable", requestID))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_LOOKUP_FAILED", "The post could not be checked", requestID))
		return
	}
	var reactionID string
	var inserted bool
	err = tx.QueryRow(ctx, `INSERT INTO testagram_local.post_reactions(post_id,user_id,emoji)
		VALUES($1,$2,$3) ON CONFLICT(post_id,user_id) DO NOTHING RETURNING id::text`, postID, userID, strings.TrimSpace(input.Emoji)).Scan(&reactionID)
	if err == nil {
		inserted = true
	} else if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, "UPDATE testagram_local.post_reactions SET emoji=$3 WHERE post_id=$1 AND user_id=$2 RETURNING id::text", postID, userID, strings.TrimSpace(input.Emoji)).Scan(&reactionID)
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("REACTION_WRITE_FAILED", "Reaction could not be saved", requestID))
		return
	}
	if inserted && authorID != userID {
		eventKey := "like:" + postID + ":" + reactionID
		_, err = tx.Exec(ctx, `INSERT INTO testagram_local.notifications(recipient_id,actor_id,post_id,kind,event_key)
			VALUES($1,$2,$3,'like',$4) ON CONFLICT(recipient_id,event_key) WHERE event_key IS NOT NULL DO NOTHING`, authorID, userID, postID, eventKey)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("REACTION_NOTIFICATION_FAILED", "Reaction and notification could not be saved", requestID))
			return
		}
	}
	var likesCount int64
	if err := tx.QueryRow(ctx, "SELECT likes_count FROM testagram_local.posts WHERE id=$1", postID).Scan(&likesCount); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("REACTION_WRITE_FAILED", "Reaction count could not be read", requestID))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("REACTION_WRITE_FAILED", "Reaction could not be committed", requestID))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"reaction_id": reactionID, "emoji": strings.TrimSpace(input.Emoji), "liked": true, "likes_count": likesCount}, "error": nil, "request_id": requestID})
}

func (s *service) socialUnreact(w http.ResponseWriter, r *http.Request) {
	userID, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	if !s.socialWriteAllowed(w, r, userID, requestID, "social-reaction", 60) {
		return
	}
	postID := strings.ToLower(r.PathValue("post_id"))
	if !socialUUIDPattern.MatchString(postID) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_POST_ID", "A valid post ID is required", requestID))
		return
	}
	tag, err := s.db.Exec(r.Context(), "DELETE FROM testagram_local.post_reactions WHERE post_id=$1 AND user_id=$2", postID, userID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("REACTION_WRITE_FAILED", "Reaction could not be removed", requestID))
		return
	}
	var likesCount int64
	if err := s.db.QueryRow(r.Context(), "SELECT likes_count FROM testagram_local.posts WHERE id=$1", postID).Scan(&likesCount); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("REACTION_WRITE_FAILED", "Reaction count could not be read", requestID))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"liked": false, "removed": tag.RowsAffected() > 0, "likes_count": likesCount}, "error": nil, "request_id": requestID})
}

func (s *service) socialReply(w http.ResponseWriter, r *http.Request) {
	userID, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	if !s.socialWriteAllowed(w, r, userID, requestID, "social-reply", 30) {
		return
	}
	postID := strings.ToLower(r.PathValue("post_id"))
	var input socialReplyRequest
	if !socialUUIDPattern.MatchString(postID) || decodeMediaJSON(w, r, &input) != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_REPLY", "A valid post ID and reply body are required", requestID))
		return
	}
	content := strings.TrimSpace(input.Content)
	if content == "" || len([]rune(content)) > 10000 {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_REPLY", "Reply text must contain 1 to 10000 characters", requestID))
		return
	}
	if input.ParentReplyID != nil && !socialUUIDPattern.MatchString(*input.ParentReplyID) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_PARENT_REPLY", "The parent reply ID is invalid", requestID))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("REPLY_WRITE_FAILED", "Reply could not be saved", requestID))
		return
	}
	defer tx.Rollback(ctx)
	authorID, err := socialCanViewPostWith(ctx, tx, userID, postID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, errorBody("POST_NOT_FOUND", "The post is unavailable", requestID))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_LOOKUP_FAILED", "The post could not be checked", requestID))
		return
	}
	if input.ParentReplyID != nil {
		var valid bool
		err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM testagram_local.replies WHERE id=$1 AND post_id=$2 AND deleted_at IS NULL)", *input.ParentReplyID, postID).Scan(&valid)
		if err != nil || !valid {
			writeJSON(w, http.StatusNotFound, errorBody("PARENT_REPLY_NOT_FOUND", "The parent reply is unavailable", requestID))
			return
		}
	}
	var replyID string
	err = tx.QueryRow(ctx, `INSERT INTO testagram_local.replies(post_id,user_id,parent_reply_id,content)
		VALUES($1,$2,$3,$4) RETURNING id::text`, postID, userID, input.ParentReplyID, content).Scan(&replyID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("REPLY_WRITE_FAILED", "Reply could not be saved", requestID))
		return
	}
	if authorID != userID {
		_, err = tx.Exec(ctx, `INSERT INTO testagram_local.notifications(recipient_id,actor_id,post_id,reply_id,kind,event_key)
			VALUES($1,$2,$3,$4,'reply',$5) ON CONFLICT(recipient_id,event_key) WHERE event_key IS NOT NULL DO NOTHING`, authorID, userID, postID, replyID, "reply:"+replyID)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("REPLY_NOTIFICATION_FAILED", "Reply and notification could not be saved", requestID))
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("REPLY_WRITE_FAILED", "Reply could not be committed", requestID))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "data": map[string]any{"reply_id": replyID, "post_id": postID, "content": content}, "error": nil, "request_id": requestID})
}

func (s *service) socialRepost(w http.ResponseWriter, r *http.Request) {
	userID, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	if !s.socialWriteAllowed(w, r, userID, requestID, "social-repost", 30) {
		return
	}
	postID := strings.ToLower(r.PathValue("post_id"))
	var input socialRepostRequest
	if !socialUUIDPattern.MatchString(postID) || decodeMediaJSON(w, r, &input) != nil || len([]rune(input.Quote)) > 10000 {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_REPOST", "A valid post ID and quote are required", requestID))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("REPOST_WRITE_FAILED", "Repost could not be saved", requestID))
		return
	}
	defer tx.Rollback(ctx)
	authorID, err := socialCanViewPostWith(ctx, tx, userID, postID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, errorBody("POST_NOT_FOUND", "The post is unavailable", requestID))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_LOOKUP_FAILED", "The post could not be checked", requestID))
		return
	}
	var repostID string
	err = tx.QueryRow(ctx, `INSERT INTO testagram_local.reposts(post_id,user_id,quote)
		VALUES($1,$2,$3) ON CONFLICT(post_id,user_id) DO NOTHING RETURNING id::text`, postID, userID, strings.TrimSpace(input.Quote)).Scan(&repostID)
	inserted := err == nil
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, "SELECT id::text FROM testagram_local.reposts WHERE post_id=$1 AND user_id=$2", postID, userID).Scan(&repostID)
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("REPOST_WRITE_FAILED", "Repost could not be saved", requestID))
		return
	}
	if inserted && authorID != userID {
		_, err = tx.Exec(ctx, `INSERT INTO testagram_local.notifications(recipient_id,actor_id,post_id,kind,event_key)
			VALUES($1,$2,$3,'repost',$4) ON CONFLICT(recipient_id,event_key) WHERE event_key IS NOT NULL DO NOTHING`, authorID, userID, postID, "repost:"+repostID)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("REPOST_NOTIFICATION_FAILED", "Repost and notification could not be saved", requestID))
			return
		}
	}
	var repostsCount int64
	if err := tx.QueryRow(ctx, "SELECT reposts_count FROM testagram_local.posts WHERE id=$1", postID).Scan(&repostsCount); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("REPOST_WRITE_FAILED", "Repost count could not be read", requestID))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("REPOST_WRITE_FAILED", "Repost could not be committed", requestID))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"repost_id": repostID, "reposted": true, "reposts_count": repostsCount}, "error": nil, "request_id": requestID})
}

func (s *service) socialUnrepost(w http.ResponseWriter, r *http.Request) {
	userID, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	if !s.socialWriteAllowed(w, r, userID, requestID, "social-repost", 30) {
		return
	}
	postID := strings.ToLower(r.PathValue("post_id"))
	if !socialUUIDPattern.MatchString(postID) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_POST_ID", "A valid post ID is required", requestID))
		return
	}
	tag, err := s.db.Exec(r.Context(), "DELETE FROM testagram_local.reposts WHERE post_id=$1 AND user_id=$2", postID, userID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("REPOST_WRITE_FAILED", "Repost could not be removed", requestID))
		return
	}
	var repostsCount int64
	if err := s.db.QueryRow(r.Context(), "SELECT reposts_count FROM testagram_local.posts WHERE id=$1", postID).Scan(&repostsCount); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("REPOST_WRITE_FAILED", "Repost count could not be read", requestID))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"reposted": false, "removed": tag.RowsAffected() > 0, "reposts_count": repostsCount}, "error": nil, "request_id": requestID})
}

func (s *service) socialBookmark(w http.ResponseWriter, r *http.Request) {
	userID, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	if !s.socialWriteAllowed(w, r, userID, requestID, "social-bookmark", 60) {
		return
	}
	postID := strings.ToLower(r.PathValue("post_id"))
	if !socialUUIDPattern.MatchString(postID) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_POST_ID", "A valid post ID is required", requestID))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("BOOKMARK_WRITE_FAILED", "Bookmark could not be saved", requestID))
		return
	}
	defer tx.Rollback(ctx)
	if _, err := socialCanViewPostWith(ctx, tx, userID, postID); errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, errorBody("POST_NOT_FOUND", "The post is unavailable", requestID))
		return
	} else if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_LOOKUP_FAILED", "The post could not be checked", requestID))
		return
	}
	_, err = tx.Exec(ctx, "INSERT INTO testagram_local.bookmarks(user_id,post_id) VALUES($1,$2) ON CONFLICT(user_id,post_id) DO NOTHING", userID, postID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("BOOKMARK_WRITE_FAILED", "Bookmark could not be saved", requestID))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("BOOKMARK_WRITE_FAILED", "Bookmark could not be committed", requestID))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"bookmarked": true}, "error": nil, "request_id": requestID})
}

func (s *service) socialUnbookmark(w http.ResponseWriter, r *http.Request) {
	userID, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	if !s.socialWriteAllowed(w, r, userID, requestID, "social-bookmark", 60) {
		return
	}
	postID := strings.ToLower(r.PathValue("post_id"))
	if !socialUUIDPattern.MatchString(postID) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_POST_ID", "A valid post ID is required", requestID))
		return
	}
	tag, err := s.db.Exec(r.Context(), "DELETE FROM testagram_local.bookmarks WHERE user_id=$1 AND post_id=$2", userID, postID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("BOOKMARK_WRITE_FAILED", "Bookmark could not be removed", requestID))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"bookmarked": false, "removed": tag.RowsAffected() > 0}, "error": nil, "request_id": requestID})
}

func (s *service) socialNotifications(w http.ResponseWriter, r *http.Request) {
	userID, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	if !s.socialWriteAllowed(w, r, userID, requestID, "social-notification", 120) {
		return
	}
	limit := 30
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_LIMIT", "limit must be between 1 and 100", requestID))
			return
		}
		limit = parsed
	}
	var beforeAt any
	var beforeID any
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		cursor, err := decodeSocialCursor(raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_CURSOR", "The notification cursor is invalid", requestID))
			return
		}
		beforeAt, beforeID = cursor.CreatedAt, cursor.ID
	}
	rows, err := s.db.Query(r.Context(), `SELECT n.id::text,n.kind,n.actor_id::text,n.post_id::text,n.reply_id::text,
		n.payload,n.read_at,n.created_at,a.username,a.display_name,a.avatar_key
		FROM testagram_local.notifications n
		LEFT JOIN testagram_local.accounts a ON a.user_id=n.actor_id
		WHERE n.recipient_id=$1 AND ($2::timestamptz IS NULL OR (n.created_at,n.id)<($2::timestamptz,$3::uuid))
		ORDER BY n.created_at DESC,n.id DESC LIMIT $4`, userID, beforeAt, beforeID, limit+1)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("NOTIFICATIONS_UNAVAILABLE", "Notifications could not be loaded", requestID))
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0, limit+1)
	var last socialCursor
	for rows.Next() {
		var id, kind string
		var actorID, postID, replyID *string
		var payload []byte
		var readAt *time.Time
		var createdAt time.Time
		var username, displayName, avatarKey *string
		if err := rows.Scan(&id, &kind, &actorID, &postID, &replyID, &payload, &readAt, &createdAt, &username, &displayName, &avatarKey); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("NOTIFICATIONS_UNAVAILABLE", "Notifications could not be loaded", requestID))
			return
		}
		var decodedPayload map[string]any
		_ = json.Unmarshal(payload, &decodedPayload)
		item := map[string]any{"id": id, "kind": kind, "actor_id": actorID, "post_id": postID, "reply_id": replyID, "payload": decodedPayload, "read_at": readAt, "created_at": createdAt}
		if actorID != nil {
			item["actor"] = map[string]any{"id": actorID, "username": username, "display_name": displayName, "avatar_key": avatarKey}
		}
		if len(items) < limit {
			last = socialCursor{CreatedAt: createdAt, ID: id}
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("NOTIFICATIONS_UNAVAILABLE", "Notifications could not be loaded", requestID))
		return
	}
	nextCursor := ""
	if len(items) > limit {
		items = items[:limit]
		nextCursor = encodeSocialCursor(last)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"items": items, "next_cursor": nextCursor}, "error": nil, "request_id": requestID})
}

func (s *service) socialMarkNotificationRead(w http.ResponseWriter, r *http.Request) {
	userID, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	if !s.socialWriteAllowed(w, r, userID, requestID, "social-notification", 120) {
		return
	}
	notificationID := strings.ToLower(r.PathValue("notification_id"))
	if !socialUUIDPattern.MatchString(notificationID) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_NOTIFICATION_ID", "A valid notification ID is required", requestID))
		return
	}
	tag, err := s.db.Exec(r.Context(), "UPDATE testagram_local.notifications SET read_at=COALESCE(read_at,now()) WHERE id=$1 AND recipient_id=$2", notificationID, userID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("NOTIFICATION_UPDATE_FAILED", "Notification could not be updated", requestID))
		return
	}
	if tag.RowsAffected() == 0 {
		writeJSON(w, http.StatusNotFound, errorBody("NOTIFICATION_NOT_FOUND", "Notification was not found", requestID))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"read": true}, "error": nil, "request_id": requestID})
}

func (s *service) socialFollowRequests(w http.ResponseWriter, r *http.Request) {
	userID, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	if !s.socialWriteAllowed(w, r, userID, requestID, "social-follow", 60) {
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT f.follower_id::text,a.username,a.display_name,a.avatar_key,f.created_at
		FROM testagram_local.follows f
		JOIN testagram_local.accounts a ON a.user_id=f.follower_id
		WHERE f.following_id=$1 AND f.status='pending' AND a.account_status='active'
		ORDER BY f.created_at DESC,f.follower_id DESC LIMIT 100`, userID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("FOLLOW_REQUESTS_UNAVAILABLE", "Follow requests could not be loaded", requestID))
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var followerID, username, displayName, avatarKey string
		var createdAt time.Time
		if err := rows.Scan(&followerID, &username, &displayName, &avatarKey, &createdAt); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("FOLLOW_REQUESTS_UNAVAILABLE", "Follow requests could not be loaded", requestID))
			return
		}
		items = append(items, map[string]any{"user_id": followerID, "username": username, "display_name": displayName, "avatar_key": avatarKey, "created_at": createdAt})
	}
	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("FOLLOW_REQUESTS_UNAVAILABLE", "Follow requests could not be loaded", requestID))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"items": items}, "error": nil, "request_id": requestID})
}

func (s *service) socialRespondFollowRequest(w http.ResponseWriter, r *http.Request, accepted bool) {
	userID, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	if !s.socialWriteAllowed(w, r, userID, requestID, "social-follow", 60) {
		return
	}
	requesterID := strings.ToLower(r.PathValue("follower_id"))
	if !socialUUIDPattern.MatchString(requesterID) || requesterID == userID {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_FOLLOW_REQUEST", "A valid requester account is required", requestID))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("FOLLOW_REQUEST_UPDATE_FAILED", "Follow request could not be updated", requestID))
		return
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, "SELECT user_id FROM testagram_local.accounts WHERE user_id IN ($1,$2) ORDER BY user_id FOR UPDATE", userID, requesterID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("FOLLOW_REQUEST_UPDATE_FAILED", "Follow request could not be updated", requestID))
		return
	}
	found := 0
	for rows.Next() {
		var ignored string
		if err := rows.Scan(&ignored); err != nil {
			rows.Close()
			writeJSON(w, http.StatusServiceUnavailable, errorBody("FOLLOW_REQUEST_UPDATE_FAILED", "Follow request could not be updated", requestID))
			return
		}
		found++
	}
	rowsErr := rows.Err()
	rows.Close()
	if rowsErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("FOLLOW_REQUEST_UPDATE_FAILED", "Follow request could not be updated", requestID))
		return
	}
	if found != 2 {
		writeJSON(w, http.StatusNotFound, errorBody("FOLLOW_REQUEST_NOT_FOUND", "Follow request was not found", requestID))
		return
	}
	var recipientStatus string
	if err := tx.QueryRow(ctx, "SELECT account_status FROM testagram_local.accounts WHERE user_id=$1", userID).Scan(&recipientStatus); err != nil || recipientStatus != "active" {
		writeJSON(w, http.StatusForbidden, errorBody("ACCOUNT_NOT_ACTIVE", "This account cannot manage follow requests", requestID))
		return
	}
	var currentStatus string
	var createdAt time.Time
	err = tx.QueryRow(ctx, "SELECT status,created_at FROM testagram_local.follows WHERE follower_id=$1 AND following_id=$2 FOR UPDATE", requesterID, userID).Scan(&currentStatus, &createdAt)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && currentStatus != "pending") {
		writeJSON(w, http.StatusNotFound, errorBody("FOLLOW_REQUEST_NOT_FOUND", "Follow request was not found", requestID))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("FOLLOW_REQUEST_UPDATE_FAILED", "Follow request could not be updated", requestID))
		return
	}
	var blocked bool
	err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM testagram_local.blocks WHERE (blocker_id=$1 AND blocked_id=$2) OR (blocker_id=$2 AND blocked_id=$1))", requesterID, userID).Scan(&blocked)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("FOLLOW_REQUEST_UPDATE_FAILED", "Follow request could not be updated", requestID))
		return
	}
	if blocked {
		_, _ = tx.Exec(ctx, "DELETE FROM testagram_local.follows WHERE follower_id=$1 AND following_id=$2", requesterID, userID)
		if err := tx.Commit(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("FOLLOW_REQUEST_UPDATE_FAILED", "Follow request could not be updated", requestID))
			return
		}
		writeJSON(w, http.StatusConflict, errorBody("FOLLOW_REQUEST_BLOCKED", "A block prevents this follow request", requestID))
		return
	}
	newStatus := "rejected"
	if accepted {
		newStatus = "accepted"
	}
	_, err = tx.Exec(ctx, "UPDATE testagram_local.follows SET status=$3,updated_at=now() WHERE follower_id=$1 AND following_id=$2", requesterID, userID, newStatus)
	if err == nil && accepted {
		eventKey := fmt.Sprintf("follow-accepted:%s:%s:%d", requesterID, userID, createdAt.UnixNano())
		_, err = tx.Exec(ctx, `INSERT INTO testagram_local.notifications(recipient_id,actor_id,kind,event_key)
			VALUES($1,$2,'follow_accepted',$3) ON CONFLICT(recipient_id,event_key) WHERE event_key IS NOT NULL DO NOTHING`, requesterID, userID, eventKey)
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("FOLLOW_REQUEST_UPDATE_FAILED", "Follow request could not be updated", requestID))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("FOLLOW_REQUEST_UPDATE_FAILED", "Follow request could not be committed", requestID))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"status": newStatus, "accepted": accepted}, "error": nil, "request_id": requestID})
}

func (s *service) socialAcceptFollowRequest(w http.ResponseWriter, r *http.Request) {
	s.socialRespondFollowRequest(w, r, true)
}

func (s *service) socialRejectFollowRequest(w http.ResponseWriter, r *http.Request) {
	s.socialRespondFollowRequest(w, r, false)
}

func (s *service) socialPostView(w http.ResponseWriter, r *http.Request) {
	userID, reqID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	if !s.socialWriteAllowed(w, r, userID, reqID, "social-post-view", 120) {
		return
	}
	postID := strings.ToLower(strings.TrimSpace(r.PathValue("post_id")))
	if !socialUUIDPattern.MatchString(postID) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_POST_ID", "Post ID is invalid", reqID))
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_VIEW_FAILED", "Post view could not be recorded", reqID))
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	if _, err := socialCanViewPostReadWith(r.Context(), tx, userID, postID); err != nil {
		http.NotFound(w, r)
		return
	}
	tag, err := tx.Exec(r.Context(), `
		INSERT INTO testagram_local.post_views(post_id,viewer_id,view_date)
		VALUES($1::uuid,$2::uuid,(now() AT TIME ZONE 'UTC')::date)
		ON CONFLICT(post_id,viewer_id,view_date) DO NOTHING
	`, postID, userID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_VIEW_FAILED", "Post view could not be recorded", reqID))
		return
	}
	recorded := tag.RowsAffected() == 1
	var views int64
	if recorded {
		err = tx.QueryRow(r.Context(), `
			UPDATE testagram_local.posts
			SET views_count=views_count+1,updated_at=updated_at
			WHERE id=$1::uuid AND deleted_at IS NULL
			RETURNING views_count
		`, postID).Scan(&views)
	} else {
		err = tx.QueryRow(r.Context(), "SELECT views_count FROM testagram_local.posts WHERE id=$1::uuid AND deleted_at IS NULL", postID).Scan(&views)
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_VIEW_FAILED", "Post view could not be recorded", reqID))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"data": map[string]any{"post_id": postID, "views_count": views, "recorded": recorded},
		"error": nil,
		"request_id": reqID,
	})
}

func (s *service) socialPostGet(w http.ResponseWriter, r *http.Request) {
	userID, reqID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	postID := strings.ToLower(strings.TrimSpace(r.PathValue("post_id")))
	if !socialUUIDPattern.MatchString(postID) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_POST_ID", "Post ID is invalid", reqID))
		return
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("SOCIAL_DATABASE_UNAVAILABLE", "Social service is unavailable", reqID))
		return
	}

	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_READ_FAILED", "Post could not be loaded", reqID))
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	authorID, err := socialCanViewPostReadWith(r.Context(), tx, userID, postID)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	var postIDValue, content, visibility, username, displayName, avatar, verifiedTier, accountType, creatorTier string
	var mediaJSON []byte
	var video bool
	var views, likes, replies, reposts, followers, following, postsCount int64
	var created, updated time.Time
	var liked, reposted, bookmarked bool
	err = tx.QueryRow(r.Context(), `
		SELECT p.id::text,p.text,p.visibility,p.media_ids,p.is_video,p.views_count,
		       p.likes_count,p.replies_count,p.reposts_count,p.created_at,p.updated_at,
		       a.username,a.display_name,a.avatar_key,a.verified_tier,a.account_type,
		       a.creator_tier,a.follower_count,a.following_count,a.posts_count,
		       EXISTS(SELECT 1 FROM testagram_local.post_reactions x WHERE x.post_id=p.id AND x.user_id=$3),
		       EXISTS(SELECT 1 FROM testagram_local.reposts x WHERE x.post_id=p.id AND x.user_id=$3),
		       EXISTS(SELECT 1 FROM testagram_local.bookmarks x WHERE x.post_id=p.id AND x.user_id=$3)
		FROM testagram_local.posts p
		JOIN testagram_local.accounts a ON a.user_id=p.author_id
		WHERE p.id=$1::uuid AND p.author_id=$2::uuid AND p.deleted_at IS NULL
	`, postID, authorID, userID).Scan(
		&postIDValue,&content,&visibility,&mediaJSON,&video,&views,
		&likes,&replies,&reposts,&created,&updated,
		&username,&displayName,&avatar,&verifiedTier,&accountType,
		&creatorTier,&followers,&following,&postsCount,&liked,&reposted,&bookmarked,
	)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_READ_FAILED", "Post could not be loaded", reqID))
		return
	}

	var mediaIDs []string
	if len(mediaJSON) > 0 {
		_ = json.Unmarshal(mediaJSON, &mediaIDs)
	}
	profile := map[string]any{
		"id": authorID, "username": username, "display_name": displayName,
		"avatar_url": avatar, "verified_tier": verifiedTier,
		"account_type": accountType, "creator_tier": creatorTier,
		"follower_count": followers, "following_count": following, "posts_count": postsCount,
	}
	post := map[string]any{
		"id": postIDValue, "user_id": authorID, "author_id": authorID,
		"content": content, "text": content, "visibility": visibility,
		"media_ids": mediaIDs, "media_urls": []string{}, "is_video": video,
		"views_count": views, "likes_count": likes, "replies_count": replies,
		"reposts_count": reposts, "created_at": created, "updated_at": updated,
		"user_profiles": profile, "profiles": profile,
		"is_liked": liked, "is_reposted": reposted, "is_bookmarked": bookmarked,
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": post, "error": nil, "request_id": reqID})
}

func (s *service) socialInteractionSummary(w http.ResponseWriter, r *http.Request) {
	userID, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	postID := strings.ToLower(r.PathValue("post_id"))
	if !socialUUIDPattern.MatchString(postID) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_POST_ID", "A valid post ID is required", requestID))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("INTERACTION_STATE_UNAVAILABLE", "Interaction state could not be loaded", requestID))
		return
	}
	defer tx.Rollback(ctx)
	if _, err := socialCanViewPostReadWith(ctx, tx, userID, postID); errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, errorBody("POST_NOT_FOUND", "The post is unavailable", requestID))
		return
	} else if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_LOOKUP_FAILED", "The post could not be checked", requestID))
		return
	}

	// Read counts, viewer state and the reaction breakdown in one indexed query
	// after the privacy check. This keeps feed-card hydration to one aggregate
	// round trip instead of issuing a separate query for every counter/state.
	var likesCount, repliesCount, repostsCount, bookmarksCount int64
	var emoji string
	var reposted, bookmarked bool
	var reactionsJSON []byte
	if err := tx.QueryRow(ctx, socialInteractionSummarySQL, postID, userID).Scan(
		&likesCount, &repliesCount, &repostsCount, &bookmarksCount,
		&emoji, &reposted, &bookmarked, &reactionsJSON,
	); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("INTERACTION_STATE_UNAVAILABLE", "Interaction summary could not be loaded", requestID))
		return
	}
	reactions := make(map[string]int64)
	if len(reactionsJSON) > 0 {
		if err := json.Unmarshal(reactionsJSON, &reactions); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("INTERACTION_STATE_UNAVAILABLE", "Reaction breakdown could not be decoded", requestID))
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("INTERACTION_STATE_UNAVAILABLE", "Interaction state could not be confirmed", requestID))
		return
	}
	userReactions := []string{}
	if emoji != "" {
		userReactions = append(userReactions, emoji)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"data": map[string]any{
			"likes": likesCount, "reposts": repostsCount, "replies": repliesCount,
			"quotes": 0, "views": 0, "shares": 0, "bookmarks": bookmarksCount,
			"reaction_total": likesCount, "reactions": reactions,
			"is_liked": emoji == "❤️", "is_reposted": reposted, "is_bookmarked": bookmarked,
			"user_reactions": userReactions,
		},
		"error": nil, "request_id": requestID,
	})
}

func (s *service) socialReactionState(w http.ResponseWriter, r *http.Request) {
	userID, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	postID := strings.ToLower(r.PathValue("post_id"))
	if !socialUUIDPattern.MatchString(postID) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_POST_ID", "A valid post ID is required", requestID))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("REACTION_STATE_UNAVAILABLE", "Reaction state could not be loaded", requestID))
		return
	}
	defer tx.Rollback(ctx)
	if _, err := socialCanViewPostReadWith(ctx, tx, userID, postID); errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, errorBody("POST_NOT_FOUND", "The post is unavailable", requestID))
		return
	} else if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_LOOKUP_FAILED", "The post could not be checked", requestID))
		return
	}
	var emoji string
	err = tx.QueryRow(ctx, "SELECT emoji FROM testagram_local.post_reactions WHERE post_id=$1 AND user_id=$2", postID, userID).Scan(&emoji)
	if errors.Is(err, pgx.ErrNoRows) {
		emoji = ""
	} else if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("REACTION_STATE_UNAVAILABLE", "Reaction state could not be loaded", requestID))
		return
	}
	var likesCount int64
	if err := tx.QueryRow(ctx, "SELECT likes_count FROM testagram_local.posts WHERE id=$1", postID).Scan(&likesCount); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("REACTION_STATE_UNAVAILABLE", "Reaction count could not be loaded", requestID))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("REACTION_STATE_UNAVAILABLE", "Reaction state could not be confirmed", requestID))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"liked": emoji != "", "emoji": emoji, "likes_count": likesCount}, "error": nil, "request_id": requestID})
}

func (s *service) socialRepostState(w http.ResponseWriter, r *http.Request) {
	userID, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	postID := strings.ToLower(r.PathValue("post_id"))
	if !socialUUIDPattern.MatchString(postID) {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_POST_ID", "A valid post ID is required", requestID))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("REPOST_STATE_UNAVAILABLE", "Repost state could not be loaded", requestID))
		return
	}
	defer tx.Rollback(ctx)
	if _, err := socialCanViewPostReadWith(ctx, tx, userID, postID); errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, errorBody("POST_NOT_FOUND", "The post is unavailable", requestID))
		return
	} else if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_LOOKUP_FAILED", "The post could not be checked", requestID))
		return
	}
	var reposted bool
	var repostsCount int64
	query := "SELECT EXISTS(SELECT 1 FROM testagram_local.reposts WHERE post_id=$1 AND user_id=$2), reposts_count FROM testagram_local.posts WHERE id=$1"
	if err := tx.QueryRow(ctx, query, postID, userID).Scan(&reposted, &repostsCount); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("REPOST_STATE_UNAVAILABLE", "Repost state could not be loaded", requestID))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("REPOST_STATE_UNAVAILABLE", "Repost state could not be confirmed", requestID))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"reposted": reposted, "reposts_count": repostsCount}, "error": nil, "request_id": requestID})
}

func (s *service) socialFollowState(w http.ResponseWriter, r *http.Request) {
	userID, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	targetID := strings.ToLower(r.PathValue("target_user_id"))
	if !socialUUIDPattern.MatchString(targetID) || targetID == userID {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_FOLLOW_TARGET", "A valid target account is required", requestID))
		return
	}
	var status string
	err := s.db.QueryRow(r.Context(), `SELECT status FROM testagram_local.follows WHERE follower_id=$1 AND following_id=$2`, userID, targetID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		status = ""
	} else if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("FOLLOW_STATE_UNAVAILABLE", "Follow state could not be loaded", requestID))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"data": map[string]any{
			"following": status == "accepted",
			"requested": status == "pending",
			"status":    status,
		},
		"error":      nil,
		"request_id": requestID,
	})
}

type socialNotificationCreateInput struct {
	Kind string `json:"kind"`
	Payload map[string]any `json:"payload"`
	EventKey string `json:"event_key"`
}

func (s *service) socialNotificationCreate(w http.ResponseWriter,r *http.Request) {
	userID,id,ok:=s.socialAuth(w,r);if !ok{return}
	if !s.socialWriteAllowed(w,r,userID,id,"social-notification-create",120){return}
	r.Body=http.MaxBytesReader(w,r.Body,12<<10);defer r.Body.Close()
	var in socialNotificationCreateInput
	if err:=decodeSingleJSON(r.Body,&in);err!=nil||len(in.Kind)<1||len(in.Kind)>48||len(in.EventKey)<1||len(in.EventKey)>256||len(in.Payload)>32 {
		writeJSON(w,http.StatusBadRequest,errorBody("INVALID_NOTIFICATION","Notification payload is invalid",id));return
	}
	payload,err:=json.Marshal(in.Payload)
	if err!=nil||len(payload)>8192 {writeJSON(w,http.StatusBadRequest,errorBody("INVALID_NOTIFICATION","Notification payload is too large",id));return}
	var notificationID string
	var created bool
	err=s.db.QueryRow(r.Context(),`INSERT INTO testagram_local.notifications(recipient_id,actor_id,kind,payload,event_key)
		VALUES($1::uuid,$1::uuid,$2,$3::jsonb,$4)
		ON CONFLICT(recipient_id,event_key) WHERE event_key IS NOT NULL DO NOTHING
		RETURNING id::text`,userID,in.Kind,string(payload),in.EventKey).Scan(&notificationID)
	if err==pgx.ErrNoRows {
		err=s.db.QueryRow(r.Context(),`SELECT id::text FROM testagram_local.notifications WHERE recipient_id=$1::uuid AND event_key=$2`,userID,in.EventKey).Scan(&notificationID)
	} else if err==nil {created=true}
	if err!=nil {writeJSON(w,http.StatusServiceUnavailable,errorBody("NOTIFICATION_CREATE_FAILED","Notification could not be saved",id));return}
	writeJSON(w,http.StatusCreated,map[string]any{"ok":true,"data":map[string]any{"id":notificationID,"created":created}})
}
