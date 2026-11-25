package main

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
)

type notificationPreferenceInput struct {
	PushSocialEnabled   *bool `json:"push_social_enabled"`
	PushWalletEnabled   *bool `json:"push_wallet_enabled"`
	PushMessagesEnabled *bool `json:"push_messages_enabled"`
}

type notificationPreferenceReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadNotificationPreferences(ctx context.Context, db notificationPreferenceReader, userID string) (bool, bool, bool, error) {
	var social, wallet, messages bool
	err := db.QueryRow(ctx, `
		SELECT COALESCE(push_social_enabled,TRUE),
		       COALESCE(push_wallet_enabled,TRUE),
		       COALESCE(push_messages_enabled,TRUE)
		FROM testagram_local.notification_preferences WHERE user_id=$1
	`, userID).Scan(&social, &wallet, &messages)
	if err == pgx.ErrNoRows {
		return true, true, true, nil
	}
	if err != nil {
		return false, false, false, err
	}
	return social, wallet, messages, nil
}

func writeNotificationPreferences(w http.ResponseWriter, r *http.Request, db notificationPreferenceReader, userID, requestID string) {
	social, wallet, messages, err := loadNotificationPreferences(r.Context(), db, userID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("NOTIFICATION_PREFERENCES_UNAVAILABLE", "Notification preferences could not be loaded", requestID))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"data": map[string]any{
			"push_social_enabled":   social,
			"push_wallet_enabled":   wallet,
			"push_messages_enabled": messages,
		},
		"error": nil, "request_id": requestID,
	})
}

func (s *service) notificationPreferences(w http.ResponseWriter, r *http.Request) {
	userID, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeNotificationPreferences(w, r, s.db, userID, requestID)
	case http.MethodPut:
		var input notificationPreferenceInput
		if err := decodeMediaJSON(w, r, &input); err != nil ||
			(input.PushSocialEnabled == nil && input.PushWalletEnabled == nil && input.PushMessagesEnabled == nil) {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_NOTIFICATION_PREFERENCES", "Set at least one notification preference", requestID))
			return
		}
		_, err := s.db.Exec(r.Context(), `
			INSERT INTO testagram_local.notification_preferences(
				user_id,push_social_enabled,push_wallet_enabled,push_messages_enabled,updated_at
			) VALUES($1,COALESCE($2,TRUE),COALESCE($3,TRUE),COALESCE($4,TRUE),now())
			ON CONFLICT(user_id) DO UPDATE SET
				push_social_enabled=COALESCE($2,notification_preferences.push_social_enabled),
				push_wallet_enabled=COALESCE($3,notification_preferences.push_wallet_enabled),
				push_messages_enabled=COALESCE($4,notification_preferences.push_messages_enabled),
				updated_at=now()
		`, userID, input.PushSocialEnabled, input.PushWalletEnabled, input.PushMessagesEnabled)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("NOTIFICATION_PREFERENCES_SAVE_FAILED", "Notification preferences could not be saved", requestID))
			return
		}
		// Return the persisted state without recursively re-reading the consumed PUT body.
		writeNotificationPreferences(w, r, s.db, userID, requestID)
	default:
		w.Header().Set("Allow", "GET, PUT, OPTIONS")
		writeJSON(w, http.StatusMethodNotAllowed, errorBody("METHOD_NOT_ALLOWED", "GET or PUT required", requestID))
	}
}
