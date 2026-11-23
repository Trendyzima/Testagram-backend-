package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

var socialMentionPattern = regexp.MustCompile(`(^|[^A-Za-z0-9_.@])@([A-Za-z0-9_][A-Za-z0-9_.]{0,28}[A-Za-z0-9_])($|[^A-Za-z0-9_])`)

// extractSocialMentionHandles returns normalized, unique local handles. It does
// not resolve identities; the transaction below resolves them against the
// canonical account table and applies block, account and visibility policy.
func extractSocialMentionHandles(content string) []string {
	matches := socialMentionPattern.FindAllStringSubmatch(content, -1)
	handles := make([]string, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		if len(match) < 3 {
			continue
		}
		handle := strings.ToLower(match[2])
		if _, exists := seen[handle]; exists {
			continue
		}
		seen[handle] = struct{}{}
		handles = append(handles, handle)
	}
	return handles
}

type socialPostCreateInput struct {
	Content    string `json:"content"`
	Visibility string `json:"visibility"`
}

func validSocialPostIdempotencyKey(value string) bool {
	if len(value) < 8 || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' || char == '.' || char == '_' ||
			char == ':' || char == '-') {
			return false
		}
	}
	return true
}

func socialPostRequestHash(input socialPostCreateInput) (string, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func existingSocialPost(ctx context.Context, queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, authorID, keyHash, requestHash string) (string, bool, error) {
	var savedHash, postID string
	err := queryer.QueryRow(ctx, `SELECT request_hash,post_id::text
		FROM testagram_local.post_create_idempotency
		WHERE author_id=$1 AND key_hash=$2`, authorID, keyHash).Scan(&savedHash, &postID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if savedHash != requestHash {
		return "", false, errors.New("idempotency key already used with different content")
	}
	return postID, true, nil
}

func (s *service) socialPostCreate(w http.ResponseWriter, r *http.Request) {
	userID, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	if !s.socialWriteAllowed(w, r, userID, requestID, "social-post-create", 10) {
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if !validSocialPostIdempotencyKey(key) {
		writeJSON(w, http.StatusBadRequest, errorBody("IDEMPOTENCY_KEY_REQUIRED", "A safe 8-128 character Idempotency-Key is required", requestID))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var input socialPostCreateInput
	if err := decoder.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_POST", "Post payload is invalid", requestID))
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_POST", "Post payload must contain one JSON object", requestID))
		return
	}
	input.Content = strings.TrimSpace(input.Content)
	input.Visibility = strings.ToLower(strings.TrimSpace(input.Visibility))
	if !utf8.ValidString(input.Content) || utf8.RuneCountInString(input.Content) < 1 || utf8.RuneCountInString(input.Content) > 5000 {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_POST_CONTENT", "Post content must contain 1-5000 valid Unicode characters", requestID))
		return
	}
	switch input.Visibility {
	case "public", "followers", "private":
	default:
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_POST_VISIBILITY", "Visibility must be public, followers, or private", requestID))
		return
	}
	requestHash, err := socialPostRequestHash(input)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_POST", "Post payload could not be normalized", requestID))
		return
	}
	keyDigest := sha256.Sum256([]byte(key))
	keyHash := hex.EncodeToString(keyDigest[:])

	// Resolve a committed retry before invoking the external moderator. A
	// provider outage must never make an already-published retry unrecoverable.
	postID, found, err := existingSocialPost(r.Context(), s.db, userID, keyHash, requestHash)
	if err != nil {
		if strings.Contains(err.Error(), "idempotency key already used") {
			writeJSON(w, http.StatusConflict, errorBody("IDEMPOTENCY_CONFLICT", "This Idempotency-Key was already used for different content", requestID))
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_LOOKUP_FAILED", "Post retry status is temporarily unavailable", requestID))
		return
	}
	if found {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"id": postID, "idempotent_replay": true}, "error": nil, "request_id": requestID})
		return
	}
	if s.moderation == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_MODERATION_UNAVAILABLE", "Post publishing is temporarily unavailable because content moderation is not configured", requestID))
		return
	}
	moderationCtx, cancelModeration := context.WithTimeout(r.Context(), 12*time.Second)
	moderation, moderationErr := s.moderation.Analyze(moderationCtx, input.Content)
	cancelModeration()
	if moderationErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_MODERATION_UNAVAILABLE", "Content moderation is temporarily unavailable; no post was published", requestID))
		return
	}
	if moderation.Action != "pass" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"ok":         false,
			"data":       moderation,
			"error":      map[string]string{"code": "CONTENT_REQUIRES_REVIEW", "message": moderation.Reason},
			"request_id": requestID,
		})
		return
	}

	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_CREATE_FAILED", "Post could not be started", requestID))
		return
	}
	defer tx.Rollback(context.Background())
	// Serialize only retries for this author/key. The second lookup handles
	// simultaneous submissions that both passed the initial read.
	if _, err = tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", userID+":"+keyHash); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_CREATE_FAILED", "Post retry could not be locked", requestID))
		return
	}
	postID, found, err = existingSocialPost(r.Context(), tx, userID, keyHash, requestHash)
	if err != nil {
		if strings.Contains(err.Error(), "idempotency key already used") {
			writeJSON(w, http.StatusConflict, errorBody("IDEMPOTENCY_CONFLICT", "This Idempotency-Key was already used for different content", requestID))
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_LOOKUP_FAILED", "Post retry status is temporarily unavailable", requestID))
		return
	}
	if found {
		if err := tx.Commit(r.Context()); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_CREATE_FAILED", "Post retry could not be confirmed", requestID))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"id": postID, "idempotent_replay": true}, "error": nil, "request_id": requestID})
		return
	}
	var createdAt time.Time
	err = tx.QueryRow(r.Context(), `INSERT INTO testagram_local.posts(author_id,text,visibility)
		VALUES($1,$2,$3) RETURNING id::text,created_at`,
		userID, input.Content, input.Visibility).Scan(&postID, &createdAt)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_CREATE_FAILED", "Post could not be saved", requestID))
		return
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO testagram_local.post_create_idempotency
		(author_id,key_hash,request_hash,post_id) VALUES($1,$2,$3,$4)`,
		userID, keyHash, requestHash, postID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_CREATE_FAILED", "Post retry record could not be saved", requestID))
		return
	}
	if input.Visibility == "public" {
		if err := s.enqueueFederationCreateTx(r.Context(), tx, userID, postID, input.Content, createdAt); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("FEDERATION_OUTBOX_UNAVAILABLE", "The public post could not be durably queued for opted-in federation", requestID))
			return
		}
	}
	// Mention notifications are committed atomically with the post. Resolve
	// handles only against active Testagram accounts; do not notify blocked
	// pairs or recipients who cannot see a protected/followers-only post.
	if input.Visibility != "private" {
		handles := extractSocialMentionHandles(input.Content)
		if len(handles) > 0 {
			_, err = tx.Exec(r.Context(), `
INSERT INTO testagram_local.notifications
  (recipient_id,actor_id,post_id,kind,payload,event_key)
SELECT DISTINCT recipient.user_id,author.user_id,$2::uuid,'mention',
       jsonb_build_object('handle',recipient.username),
       'mention:' || $2::text || ':' || recipient.user_id::text
FROM unnest($3::text[]) AS mentioned(handle)
JOIN testagram_local.accounts recipient
  ON lower(recipient.username)=mentioned.handle
JOIN testagram_local.accounts author ON author.user_id=$1::uuid
WHERE recipient.account_status='active'
  AND recipient.user_id<>author.user_id
  AND NOT EXISTS (
    SELECT 1 FROM testagram_local.blocks b
    WHERE (b.blocker_id=author.user_id AND b.blocked_id=recipient.user_id)
       OR (b.blocker_id=recipient.user_id AND b.blocked_id=author.user_id)
  )
  AND (
    ($4::text='public' AND NOT author.protected_account)
    OR EXISTS (
      SELECT 1 FROM testagram_local.follows f
      WHERE f.follower_id=recipient.user_id
        AND f.following_id=author.user_id
        AND f.status='accepted'
    )
  )
ON CONFLICT (recipient_id,event_key) WHERE event_key IS NOT NULL DO NOTHING`,
				userID, postID, handles, input.Visibility)
			if err != nil {
				writeJSON(w, http.StatusServiceUnavailable, errorBody("MENTION_NOTIFICATION_FAILED", "Mention notifications could not be saved", requestID))
				return
			}
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("POST_CREATE_FAILED", "Post could not be committed", requestID))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"ok": true,
		"data": map[string]any{
			"id":                postID,
			"author_id":         userID,
			"content":           input.Content,
			"visibility":        input.Visibility,
			"created_at":        createdAt.UTC().Format(time.RFC3339Nano),
			"idempotent_replay": false,
		},
		"error":      nil,
		"request_id": requestID,
	})
}
