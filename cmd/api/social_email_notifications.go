package main

import (
	"context"
	"html"
	"net/http"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
)

const socialEmailConsentVersion = "2026-11-23-v1"

type socialEmailPreferenceInput struct {
	NewFollowerEmailEnabled *bool `json:"new_follower_email_enabled"`
	NewPostEmailEnabled     *bool `json:"new_post_email_enabled"`
}

func (s *service) writeSocialEmailPreferences(w http.ResponseWriter, r *http.Request, userID, requestID string) {
	var followEmails, postEmails, promptRequired bool
	var version string
	err := s.db.QueryRow(r.Context(), `
		SELECT COALESCE(p.new_follower_email_enabled,FALSE),
		       COALESCE(p.new_post_email_enabled,FALSE),
		       COALESCE(p.consent_version,$2),
		       (p.user_id IS NULL)
		FROM (SELECT $1::uuid AS user_id) owner
		LEFT JOIN testagram_local.email_notification_preferences p USING(user_id)
	`, userID, socialEmailConsentVersion).Scan(&followEmails, &postEmails, &version, &promptRequired)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("EMAIL_PREFERENCES_UNAVAILABLE", "Email preferences could not be loaded", requestID))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"data": map[string]any{
			"new_follower_email_enabled": followEmails,
			"new_post_email_enabled":     postEmails,
			"consent_version":            version,
			"consent_prompt_required":    promptRequired,
		},
		"error": nil, "request_id": requestID,
	})
}

// socialEmailPreferences exposes the authenticated account owner's explicit
// email opt-ins. Missing rows are deliberately treated as both preferences off.
func (s *service) socialEmailPreferences(w http.ResponseWriter, r *http.Request) {
	userID, requestID, ok := s.socialAuth(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.writeSocialEmailPreferences(w, r, userID, requestID)
	case http.MethodPut:
		var input socialEmailPreferenceInput
		if err := decodeMediaJSON(w, r, &input); err != nil ||
			(input.NewFollowerEmailEnabled == nil && input.NewPostEmailEnabled == nil) {
			writeJSON(w, http.StatusBadRequest, errorBody("INVALID_EMAIL_PREFERENCES", "Set at least one email notification preference", requestID))
			return
		}
		tx, err := s.db.Begin(r.Context())
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("EMAIL_PREFERENCES_SAVE_FAILED", "Email preferences could not be saved", requestID))
			return
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		_, err = tx.Exec(r.Context(), `
			INSERT INTO testagram_local.email_notification_preferences(
				user_id,new_follower_email_enabled,new_post_email_enabled,consent_version,
				new_follower_consent_at,new_follower_consent_withdrawn_at,
				new_post_consent_at,new_post_consent_withdrawn_at,updated_at
			)
			VALUES(
				$1,COALESCE($2,FALSE),COALESCE($3,FALSE),$4,
				CASE WHEN COALESCE($2,FALSE) THEN now() END,NULL,
				CASE WHEN COALESCE($3,FALSE) THEN now() END,NULL,now()
			)
			ON CONFLICT(user_id) DO UPDATE SET
				new_follower_email_enabled=COALESCE($2,email_notification_preferences.new_follower_email_enabled),
				new_post_email_enabled=COALESCE($3,email_notification_preferences.new_post_email_enabled),
				new_follower_consent_at=CASE
					WHEN $2 IS TRUE THEN now()
					ELSE email_notification_preferences.new_follower_consent_at END,
				new_follower_consent_withdrawn_at=CASE
					WHEN $2 IS TRUE THEN NULL
					WHEN $2 IS FALSE THEN now()
					ELSE email_notification_preferences.new_follower_consent_withdrawn_at END,
				new_post_consent_at=CASE
					WHEN $3 IS TRUE THEN now()
					ELSE email_notification_preferences.new_post_consent_at END,
				new_post_consent_withdrawn_at=CASE
					WHEN $3 IS TRUE THEN NULL
					WHEN $3 IS FALSE THEN now()
					ELSE email_notification_preferences.new_post_consent_withdrawn_at END,
				consent_version=CASE WHEN COALESCE($2,FALSE) OR COALESCE($3,FALSE)
					THEN $4 ELSE email_notification_preferences.consent_version END,
				updated_at=now()
		`, userID, input.NewFollowerEmailEnabled, input.NewPostEmailEnabled, socialEmailConsentVersion)
		if err == nil && input.NewFollowerEmailEnabled != nil && !*input.NewFollowerEmailEnabled {
			_, err = tx.Exec(r.Context(), `
				UPDATE testagram_mail.outbox
				SET status='dead',last_error_code='CONSENT_WITHDRAWN',payload_ciphertext='',payload_nonce='',updated_at=now()
				WHERE preference_user_id=$1 AND preference_category='new_follower' AND status='pending'
			`, userID)
		}
		if err == nil && input.NewPostEmailEnabled != nil && !*input.NewPostEmailEnabled {
			_, err = tx.Exec(r.Context(), `
				UPDATE testagram_mail.outbox
				SET status='dead',last_error_code='CONSENT_WITHDRAWN',updated_at=now()
				WHERE preference_user_id=$1 AND preference_category='new_post' AND status='pending'
			`, userID)
		}
		if err == nil {
			err = tx.Commit(r.Context())
		}
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("EMAIL_PREFERENCES_SAVE_FAILED", "Email preferences could not be saved", requestID))
			return
		}
		s.writeSocialEmailPreferences(w, r, userID, requestID)
	default:
		w.Header().Set("Allow", "GET, PUT, OPTIONS")
		writeJSON(w, http.StatusMethodNotAllowed, errorBody("METHOD_NOT_ALLOWED", "GET or PUT required", requestID))
	}
}

func enqueueFollowActivityEmail(ctx context.Context, tx pgx.Tx, actorID, recipientID, kind string) error {
	var recipient, actorName string
	err := tx.QueryRow(ctx, `
		SELECT recipient.email,COALESCE(NULLIF(actor.display_name,''),'@'||actor.username)
		FROM testagram_auth.identities recipient
		JOIN testagram_local.accounts target ON target.user_id=recipient.user_id
		JOIN testagram_local.accounts actor ON actor.user_id=$1
		JOIN testagram_local.email_notification_preferences pref ON pref.user_id=recipient.user_id
		WHERE recipient.user_id=$2
		  AND recipient.email_verified_at IS NOT NULL
		  AND recipient.disabled_at IS NULL
		  AND target.account_status='active'
		  AND actor.account_status='active'
		  AND pref.new_follower_email_enabled=TRUE
	`, actorID, recipientID).Scan(&recipient, &actorName)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	action := "started following you"
	subject := "You have a new Testagram follower"
	if kind == "follow_request" {
		action = "requested to follow you"
		subject = "You have a Testagram follow request"
	}
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("AUTH_PUBLIC_BASE_URL")), "/")
	link := ""
	if base != "" {
		link = "<p><a href=\"" + html.EscapeString(base) + "\">Open Testagram</a></p>"
	}
	body := "<p><strong>" + html.EscapeString(actorName) + "</strong> " + action + ".</p>" + link +
		"<p>You received this email because you enabled follower activity emails in Testagram settings.</p>"
	return enqueueSocialEmailWith(ctx, tx, recipient, recipientID, "new_follower", subject, body)
}

func createFollowerPostNotifications(ctx context.Context, tx pgx.Tx, authorID, postID, visibility string) error {
	if visibility != "public" && visibility != "followers" {
		return nil
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO testagram_local.notifications(recipient_id,actor_id,post_id,kind,payload,event_key)
		SELECT f.follower_id,$1,$2,'post',jsonb_build_object('post_id',$2),
		       'post:' || $2::text || ':' || f.follower_id::text
		FROM testagram_local.follows f
		JOIN testagram_local.accounts follower ON follower.user_id=f.follower_id
		WHERE f.following_id=$1 AND f.status='accepted'
		  AND f.follower_id<>$1 AND follower.account_status='active'
		  AND NOT EXISTS (
			SELECT 1 FROM testagram_local.blocks b
			WHERE (b.blocker_id=$1 AND b.blocked_id=f.follower_id)
			   OR (b.blocker_id=f.follower_id AND b.blocked_id=$1)
		  )
		ON CONFLICT(recipient_id,event_key) WHERE event_key IS NOT NULL DO NOTHING
	`, authorID, postID)
	return err
}

func enqueueNewPostActivityEmails(ctx context.Context, tx pgx.Tx, authorID, visibility, content string) error {
	if visibility != "public" && visibility != "followers" {
		return nil
	}
	var authorName string
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(NULLIF(display_name,''),'@'||username)
		FROM testagram_local.accounts
		WHERE user_id=$1 AND account_status='active'
	`, authorID).Scan(&authorName); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT i.email,f.follower_id::text
		FROM testagram_local.follows f
		JOIN testagram_local.accounts follower ON follower.user_id=f.follower_id
		JOIN testagram_auth.identities i ON i.user_id=f.follower_id
		JOIN testagram_local.email_notification_preferences pref ON pref.user_id=f.follower_id
		JOIN testagram_local.accounts author ON author.user_id=f.following_id
		WHERE f.following_id=$1 AND f.status='accepted'
		  AND f.follower_id<>$1
		  AND follower.account_status='active'
		  AND author.account_status='active'
		  AND i.email_verified_at IS NOT NULL AND i.disabled_at IS NULL
		  AND pref.new_post_email_enabled=TRUE
		  AND NOT EXISTS (
			SELECT 1 FROM testagram_local.blocks b
			WHERE (b.blocker_id=$1 AND b.blocked_id=f.follower_id)
			   OR (b.blocker_id=f.follower_id AND b.blocked_id=$1)
		  )
		ORDER BY i.email
	`, authorID)
	if err != nil {
		return err
	}
	type socialEmailRecipient struct {
		email  string
		userID string
	}
	recipients := make([]socialEmailRecipient, 0)
	for rows.Next() {
		var recipient socialEmailRecipient
		if err := rows.Scan(&recipient.email, &recipient.userID); err != nil {
			rows.Close()
			return err
		}
		recipients = append(recipients, recipient)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	snippet := strings.TrimSpace(content)
	runes := []rune(snippet)
	if len(runes) > 320 {
		snippet = string(runes[:320]) + "…"
	}
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("AUTH_PUBLIC_BASE_URL")), "/")
	link := ""
	if base != "" {
		link = "<p><a href=\"" + html.EscapeString(base) + "\">Open Testagram to read and reply</a></p>"
	}
	body := "<p><strong>" + html.EscapeString(authorName) + "</strong> shared a new post:</p>" +
		"<blockquote>" + html.EscapeString(snippet) + "</blockquote>" + link +
		"<p>You received this email because you enabled new-post emails from accounts you follow in Testagram settings.</p>"
	for _, recipient := range recipients {
		if err := enqueueSocialEmailWith(ctx, tx, recipient.email, recipient.userID, "new_post", "A person you follow shared a new Testagram post", body); err != nil {
			return err
		}
	}
	return nil
}
