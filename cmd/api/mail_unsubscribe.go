package main

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func unsubscribeTokenHash(token string) ([]byte, error) {
	if len(token) > 128 { return nil, errors.New("invalid unsubscribe token") }
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 { return nil, errors.New("invalid unsubscribe token") }
	hash := sha256.Sum256(raw)
	return hash[:], nil
}

func (s *service) socialEmailUnsubscribe(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; form-action 'self'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid unsubscribe request", http.StatusBadRequest)
			return
		}
		if token == "" { token = strings.TrimSpace(r.FormValue("token")) }
	}
	hash, err := unsubscribeTokenHash(token)
	if err != nil || s.db == nil {
		http.Error(w, "This unsubscribe link is invalid or expired.", http.StatusNotFound)
		return
	}
	if r.Method == http.MethodGet {
		var category string
		err := s.db.QueryRow(r.Context(), `
			SELECT preference_category FROM testagram_mail.social_unsubscribe_tokens
			WHERE token_hash=$1 AND expires_at>now()
		`, hash).Scan(&category)
		if err != nil {
			http.Error(w, "This unsubscribe link is invalid or expired.", http.StatusNotFound)
			return
		}
		label := "social activity"
		if category == "new_follower" { label = "new follower" }
		if category == "new_post" { label = "new post" }
		_, _ = w.Write([]byte(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Unsubscribe from Testagram</title><body style="font-family:Arial,sans-serif;max-width:560px;margin:48px auto;padding:0 20px;color:#182230"><h1>Unsubscribe from Testagram emails</h1><p>Stop receiving ` + html.EscapeString(label) + ` activity emails?</p><form method="post" action="/api/email/unsubscribe?token=` + html.EscapeString(token) + `"><button type="submit" style="padding:12px 18px;background:#155eef;color:white;border:0;border-radius:8px">Unsubscribe</button></form></body></html>`))
		return
	}

	tx, err := s.db.Begin(r.Context())
	if err != nil {
		http.Error(w, "Unsubscribe is temporarily unavailable. Please try again.", http.StatusServiceUnavailable)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	var userID, category string
	var alreadyUsed bool
	err = tx.QueryRow(r.Context(), `
		SELECT user_id::text,preference_category,used_at IS NOT NULL
		FROM testagram_mail.social_unsubscribe_tokens
		WHERE token_hash=$1 AND expires_at>now()
		FOR UPDATE
	`, hash).Scan(&userID, &category, &alreadyUsed)
	if err != nil {
		http.Error(w, "This unsubscribe link is invalid or expired.", http.StatusNotFound)
		return
	}
	if alreadyUsed {
		_ = tx.Commit(r.Context())
		_, _ = w.Write([]byte("<!doctype html><html lang=\"en\"><meta charset=\"utf-8\"><title>Unsubscribed</title><body><h1>You are unsubscribed</h1><p>This email preference was already turned off.</p></body></html>"))
		return
	}
	if category == "new_follower" {
		_, err = tx.Exec(r.Context(), `
			UPDATE testagram_local.email_notification_preferences
			SET new_follower_email_enabled=FALSE,
			    new_follower_consent_withdrawn_at=CASE WHEN new_follower_email_enabled THEN now() ELSE new_follower_consent_withdrawn_at END,
			    updated_at=now()
			WHERE user_id=$1::uuid
		`, userID)
	} else {
		_, err = tx.Exec(r.Context(), `
			UPDATE testagram_local.email_notification_preferences
			SET new_post_email_enabled=FALSE,
			    new_post_consent_withdrawn_at=CASE WHEN new_post_email_enabled THEN now() ELSE new_post_consent_withdrawn_at END,
			    updated_at=now()
			WHERE user_id=$1::uuid
		`, userID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `
			UPDATE testagram_mail.outbox
			SET status='dead',lease_until=NULL,last_error_code='CONSENT_WITHDRAWN',
			    payload_ciphertext='',payload_nonce='',updated_at=now()
			WHERE preference_user_id=$1::uuid AND preference_category=$2 AND status='pending'
		`, userID, category)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `
			UPDATE testagram_mail.social_unsubscribe_tokens SET used_at=now()
			WHERE token_hash=$1 AND used_at IS NULL
		`, hash)
	}
	if err != nil {
		http.Error(w, "Unsubscribe is temporarily unavailable. Please try again.", http.StatusServiceUnavailable)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		http.Error(w, "Unsubscribe is temporarily unavailable. Please try again.", http.StatusServiceUnavailable)
		return
	}
	_, _ = w.Write([]byte("<!doctype html><html lang=\"en\"><meta charset=\"utf-8\"><title>Unsubscribed</title><body><h1>You are unsubscribed</h1><p>You will no longer receive this category of Testagram activity email.</p></body></html>"))
}

var _ = pgx.ErrNoRows
var _ = time.Second
