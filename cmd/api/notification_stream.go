package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type notificationStreamCursor struct {
	CreatedAt time.Time `json:"created_at"`
	ID        string    `json:"id"`
}

type notificationStreamItem struct {
	ID        string         `json:"id"`
	ActorID   string         `json:"actor_id,omitempty"`
	PostID    string         `json:"post_id,omitempty"`
	ReplyID   string         `json:"reply_id,omitempty"`
	Kind      string         `json:"kind"`
	Payload   map[string]any `json:"payload"`
	ReadAt    *time.Time     `json:"read_at,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

func encodeNotificationStreamCursor(cursor notificationStreamCursor) string {
	raw, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeNotificationStreamCursor(value string) (notificationStreamCursor, error) {
	var cursor notificationStreamCursor
	if value == "" {
		return cursor, nil
	}
	if len(value) > 512 {
		return cursor, errors.New("notification cursor is too long")
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || json.Unmarshal(raw, &cursor) != nil ||
		cursor.CreatedAt.IsZero() || !socialUUIDPattern.MatchString(cursor.ID) {
		return notificationStreamCursor{}, errors.New("invalid notification cursor")
	}
	return cursor, nil
}

// socialNotificationStream reads the canonical Backend inbox directly. It is
// intentionally read-only: the REST inbox remains authoritative for read
// state, while SSE only signals the client to refresh its inbox.
func (s *service) socialNotificationStream(w http.ResponseWriter, r *http.Request) {
	uid, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	cursor, err := decodeNotificationStreamCursor(strings.TrimSpace(r.Header.Get("Last-Event-ID")))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("INVALID_NOTIFICATION_CURSOR", "Notification cursor is invalid", requestID))
		return
	}
	if cursor.CreatedAt.IsZero() {
		if err := s.db.QueryRow(r.Context(), "SELECT now() - interval '30 seconds'").Scan(&cursor.CreatedAt); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("NOTIFICATIONS_UNAVAILABLE", "Notifications are temporarily unavailable", requestID))
			return
		}
		cursor.CreatedAt = cursor.CreatedAt.UTC()
		cursor.ID = "00000000-0000-0000-0000-000000000000"
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, errorBody("STREAMING_UNSUPPORTED", "This connection cannot stream notifications", requestID))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	poll := time.NewTicker(time.Second)
	heartbeat := time.NewTicker(15 * time.Second)
	defer poll.Stop()
	defer heartbeat.Stop()

	for {
		delivered, next, streamErr := s.deliverNotificationStreamItem(ctx, uid, cursor, w, flusher)
		if streamErr != nil {
			if !errors.Is(streamErr, context.Canceled) && !errors.Is(streamErr, context.DeadlineExceeded) {
				log.Printf("notification stream: %v", streamErr)
			}
			return
		}
		if delivered {
			cursor = next
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
		case <-heartbeat.C:
			if _, err := w.Write([]byte(": keep-alive\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *service) deliverNotificationStreamItem(ctx context.Context, user string, cursor notificationStreamCursor, w http.ResponseWriter, flusher http.Flusher) (bool, notificationStreamCursor, error) {
	var item notificationStreamItem
	err := s.db.QueryRow(ctx, `
		SELECT id::text, COALESCE(actor_id::text,''), COALESCE(post_id::text,''),
		       COALESCE(reply_id::text,''), kind, payload, read_at, created_at
		FROM testagram_local.notifications
		WHERE recipient_id=$1
		  AND (created_at > $2 OR (created_at = $2 AND id > $3::uuid))
		ORDER BY created_at ASC, id ASC
		LIMIT 1
	`, user, cursor.CreatedAt, cursor.ID).Scan(
		&item.ID, &item.ActorID, &item.PostID, &item.ReplyID,
		&item.Kind, &item.Payload, &item.ReadAt, &item.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, cursor, nil
	}
	if err != nil {
		return false, cursor, err
	}
	next := notificationStreamCursor{CreatedAt: item.CreatedAt.UTC(), ID: item.ID}
	data, err := json.Marshal(item)
	if err != nil {
		return false, cursor, err
	}
	frame := "event: notification\nid: " + encodeNotificationStreamCursor(next) + "\ndata: " + string(data) + "\n\n"
	if _, err := w.Write([]byte(frame)); err != nil {
		return false, cursor, err
	}
	flusher.Flush()
	return true, next, nil
}
