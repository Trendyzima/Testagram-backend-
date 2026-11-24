package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"os"
	"strings"
	"time"
)

type mailjetDeliveryEvent struct {
	Event          string      `json:"event"`
	Time           int64       `json:"time"`
	MessageGUID    string      `json:"Message_GUID"`
	MessageID      json.Number `json:"MessageID"`
	CustomID       string      `json:"CustomID"`
	Email          string      `json:"email"`
	Blocked        bool        `json:"blocked"`
	HardBounce     bool        `json:"hard_bounce"`
	ErrorRelatedTo string      `json:"error_related_to"`
	Error          string      `json:"error"`
	SMTPReply      string      `json:"smtp_reply"`
}

func (s *service) mailjetDeliveryEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	username, password, ok := r.BasicAuth()
	expectedUser := strings.TrimSpace(os.Getenv("MAILJET_SMTP_USERNAME"))
	expectedPass := os.Getenv("MAILJET_SMTP_PASSWORD")
	if !ok || expectedUser == "" || expectedPass == "" ||
		len(username) != len(expectedUser) || len(password) != len(expectedPass) ||
		subtle.ConstantTimeCompare([]byte(username), []byte(expectedUser)) != 1 ||
		subtle.ConstantTimeCompare([]byte(password), []byte(expectedPass)) != 1 {
		w.Header().Set("WWW-Authenticate", `Basic realm="Testagram Mailjet events"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.db == nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.UseNumber()
	var events []mailjetDeliveryEvent
	if err := decoder.Decode(&events); err != nil || len(events) > 100 {
		http.Error(w, "invalid event batch", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, "invalid event body", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	for _, event := range events {
		eventType := strings.ToLower(strings.TrimSpace(event.Event))
		switch eventType {
		case "sent", "bounce", "blocked", "spam":
		default:
			continue
		}
		customID := strings.TrimSpace(event.CustomID)
		if !strings.HasPrefix(customID, "testagram-") || len(customID) > 64 ||
			strings.TrimSpace(event.Email) == "" || len(event.Email) > 320 {
			// This Mailjet account may contain other traffic; only ingest events
			// explicitly tagged by this application.
			continue
		}
		if _, err := mail.ParseAddress(event.Email); err != nil {
			continue
		}
		fingerprint, err := mailRecipientFingerprint(event.Email)
		if err != nil {
			http.Error(w, "mail event service unavailable", http.StatusServiceUnavailable)
			return
		}
		providerEventID := mailjetEventFingerprint(event)
		bounceType := ""
		if eventType == "bounce" {
			bounceType = "soft"
			if event.HardBounce {
				bounceType = "hard"
			}
		}
		rejectionParty := strings.ToLower(strings.TrimSpace(event.ErrorRelatedTo))
		if len(rejectionParty) > 128 {
			rejectionParty = rejectionParty[:128]
		}
		reason := strings.TrimSpace(event.Error)
		if len(reason) > 512 {
			reason = reason[:512]
		}
		smtpReply := strings.TrimSpace(event.SMTPReply)
		if len(smtpReply) > 512 {
			smtpReply = smtpReply[:512]
		}
		_, err = s.db.Exec(ctx, `
			INSERT INTO testagram_mail.delivery_events(
				provider_event_id,provider_message_id,event_type,recipient_fingerprint,
				smtp_status_code,bounce_type,rejection_reason,rejection_party
			) VALUES($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),NULLIF($7,''),NULLIF($8,''))
			ON CONFLICT(provider_event_id) DO NOTHING
		`, providerEventID, customID, "mailjet."+eventType, fingerprint, smtpReply, bounceType, reason, rejectionParty)
		if err != nil {
			http.Error(w, "event persistence failed", http.StatusServiceUnavailable)
			return
		}
	}
	if err := s.reconcileMailjetEvents(ctx); err != nil {
		http.Error(w, "event reconciliation pending", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func mailjetEventFingerprint(event mailjetDeliveryEvent) string {
	raw := fmt.Sprintf("%s|%s|%s|%s|%s|%d",
		strings.ToLower(strings.TrimSpace(event.Event)),
		strings.TrimSpace(event.CustomID),
		strings.ToLower(strings.TrimSpace(event.Email)),
		strings.TrimSpace(event.MessageGUID),
		event.MessageID.String(),
		event.Time,
	)
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func (s *service) reconcileMailjetEvents(ctx context.Context) error {
	if s.db == nil {
		return errors.New("mail database unavailable")
	}
	_, err := s.db.Exec(ctx, `
		WITH candidates AS (
			SELECT e.id,e.provider_event_id,e.provider_message_id,e.event_type,
			       e.recipient_fingerprint,e.bounce_type,e.rejection_reason,e.rejection_party
			FROM testagram_mail.delivery_events e
			WHERE e.processed_at IS NULL
			  AND e.event_type IN ('mailjet.sent','mailjet.bounce','mailjet.blocked','mailjet.spam')
			ORDER BY e.received_at,e.id
			LIMIT 100
			FOR UPDATE SKIP LOCKED
		),
		matched AS (
			UPDATE testagram_mail.outbox o
			SET delivery_status=CASE c.event_type
					WHEN 'mailjet.sent' THEN 'delivered'
					WHEN 'mailjet.bounce' THEN 'bounced'
					WHEN 'mailjet.blocked' THEN 'rejected'
					WHEN 'mailjet.spam' THEN 'complained'
					ELSE o.delivery_status
				END,
				delivered_at=CASE WHEN c.event_type='mailjet.sent' THEN now() ELSE o.delivered_at END,
				delivery_failed_at=CASE WHEN c.event_type IN ('mailjet.bounce','mailjet.blocked') THEN now() ELSE o.delivery_failed_at END,
				complained_at=CASE WHEN c.event_type='mailjet.spam' THEN now() ELSE o.complained_at END,
				updated_at=now()
			FROM candidates c
			WHERE o.provider_message_id=c.provider_message_id
			  AND o.recipient_fingerprint=c.recipient_fingerprint
			RETURNING c.id,c.provider_event_id,c.event_type,c.recipient_fingerprint,
			          c.bounce_type,c.rejection_party
		),
		suppressions AS (
			INSERT INTO testagram_mail.recipient_suppressions(recipient_fingerprint,reason,source_event_id)
			SELECT recipient_fingerprint,
			       CASE
			         WHEN event_type='mailjet.spam' THEN 'complaint'
			         WHEN event_type='mailjet.blocked' THEN 'rejected'
			         ELSE 'hard_bounce'
			       END,
			       provider_event_id
			FROM matched
			WHERE event_type='mailjet.spam'
			   OR (event_type='mailjet.blocked' AND rejection_party='recipient')
			   OR (event_type='mailjet.bounce' AND bounce_type='hard')
			ON CONFLICT(recipient_fingerprint) DO NOTHING
			RETURNING recipient_fingerprint
		)
		UPDATE testagram_mail.delivery_events e
		SET processed_at=now()
		WHERE e.id IN (SELECT id FROM matched)
	`)
	return err
}
