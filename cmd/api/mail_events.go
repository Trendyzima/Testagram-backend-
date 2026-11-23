package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/mail"
	"net/mail"
	"os"
	"strings"
	"time"
)

type cloudflareDeliveryEvent struct {
	Type string `json:"type"`
	Source struct {
		Type string `json:"type"`
		Domain string `json:"domain"`
	} `json:"source"`
	Payload struct {
		EventID string `json:"eventId"`
		MessageID string `json:"messageId"`
		Recipient string `json:"recipient"`
		Terminal bool `json:"terminal"`
		Delivery struct {
			Status string `json:"status"`
			SMTPStatusCode string `json:"smtpStatusCode"`
		} `json:"delivery"`
		Bounce struct {
			Type string `json:"type"`
		} `json:"bounce"`
		Rejection struct {
			Reason string `json:"reason"`
			Party string `json:"party"`
		} `json:"rejection"`
	} `json:"payload"`
}

func mailRecipientFingerprint(recipient string) ([]byte, error) {
	key, err := hex.DecodeString(strings.TrimSpace(os.Getenv("TESTAGRAM_MAIL_ENCRYPTION_KEY")))
	if err != nil || len(key) != 32 {
		return nil, errors.New("mail encryption key unavailable")
	}
	parsed, err := mail.ParseAddress(recipient)
	if err != nil { return nil, errors.New("invalid recipient address") }
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(strings.ToLower(strings.TrimSpace(parsed.Address))))
	return mac.Sum(nil), nil
}

func (s *service) cloudflareMailEvent(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	expected := strings.TrimSpace(os.Getenv("TESTAGRAM_CLOUDFLARE_EVENTS_TOKEN"))
	provided := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if len(expected) < 32 || provided == "" || len(expected) != len(provided) ||
		subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var event cloudflareDeliveryEvent
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&event); err != nil {
		http.Error(w, "invalid event", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, "invalid event body", http.StatusBadRequest)
		return
	}
	allowed := map[string]bool{
		"cf.email.sending.message.delivered": true,
		"cf.email.sending.message.deferred": true,
		"cf.email.sending.message.bounced": true,
		"cf.email.sending.message.failed": true,
		"cf.email.sending.message.rejected": true,
		"cf.email.sending.message.complained": true,
	}
	sender, senderErr := mail.ParseAddress(os.Getenv("AUTH_FROM_EMAIL"))
	senderParts := strings.Split(sender.Address, "@")
	if senderErr != nil || len(senderParts) != 2 || event.Source.Type != "email.sending" ||
		!strings.EqualFold(event.Source.Domain, senderParts[1]) ||
		!allowed[event.Type] || strings.TrimSpace(event.Payload.EventID) == "" ||
		strings.TrimSpace(event.Payload.MessageID) == "" || len(event.Payload.EventID) > 128 ||
		len(event.Payload.MessageID) > 512 || len(event.Payload.Recipient) > 320 {
		http.Error(w, "invalid event fields", http.StatusBadRequest)
		return
	}
	if _, err := mail.ParseAddress(event.Payload.Recipient); err != nil {
		http.Error(w, "invalid recipient", http.StatusBadRequest)
		return
	}
	fingerprint, err := mailRecipientFingerprint(event.Payload.Recipient)
	if err != nil {
		http.Error(w, "mail event service unavailable", http.StatusServiceUnavailable)
		return
	}
	if s.db == nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	_, err = s.db.Exec(ctx, `
		INSERT INTO testagram_mail.delivery_events(
			provider_event_id,provider_message_id,event_type,recipient_fingerprint,smtp_status_code,bounce_type,rejection_reason,rejection_party
		) VALUES($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),NULLIF($7,''),NULLIF($8,''))
		ON CONFLICT(provider_event_id) DO NOTHING
	`, event.Payload.EventID, event.Payload.MessageID, event.Type, fingerprint,
		event.Payload.Delivery.SMTPStatusCode, event.Payload.Bounce.Type, event.Payload.Rejection.Reason, event.Payload.Rejection.Party)
	if err != nil {
		http.Error(w, "event persistence failed", http.StatusServiceUnavailable)
		return
	}
	if err := s.reconcileCloudflareMailEvents(ctx); err != nil {
		http.Error(w, "event reconciliation pending", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte("{\"accepted\":true}\n"))
}

func (s *service) reconcileCloudflareMailEvents(ctx context.Context) error {
	if s.db == nil {
		return errors.New("mail database unavailable")
	}
	_, err := s.db.Exec(ctx, `
		WITH candidates AS (
			SELECT e.id,e.provider_event_id,e.provider_message_id,e.event_type,
			       e.recipient_fingerprint,e.bounce_type,e.rejection_reason,e.rejection_party
			FROM testagram_mail.delivery_events e
			WHERE e.processed_at IS NULL
			ORDER BY e.received_at,e.id
			LIMIT 100
			FOR UPDATE SKIP LOCKED
		),
		matched AS (
			UPDATE testagram_mail.outbox o
			SET delivery_status=CASE
				WHEN o.delivery_status IN ('delivered','bounced','failed','rejected','complained')
				 AND c.event_type <> 'cf.email.sending.message.complained' THEN o.delivery_status
				ELSE CASE c.event_type
					WHEN 'cf.email.sending.message.delivered' THEN 'delivered'
					WHEN 'cf.email.sending.message.deferred' THEN 'deferred'
					WHEN 'cf.email.sending.message.bounced' THEN 'bounced'
					WHEN 'cf.email.sending.message.failed' THEN 'failed'
					WHEN 'cf.email.sending.message.rejected' THEN 'rejected'
					WHEN 'cf.email.sending.message.complained' THEN 'complained'
				END
			END,
			delivered_at=CASE WHEN c.event_type='cf.email.sending.message.delivered' THEN now() ELSE o.delivered_at END,
			delivery_failed_at=CASE WHEN c.event_type IN (
				'cf.email.sending.message.bounced','cf.email.sending.message.failed','cf.email.sending.message.rejected'
			) THEN now() ELSE o.delivery_failed_at END,
			complained_at=CASE WHEN c.event_type='cf.email.sending.message.complained' THEN now() ELSE o.complained_at END,
			updated_at=now()
			FROM candidates c
			WHERE o.provider_message_id=c.provider_message_id
			  AND o.recipient_fingerprint=c.recipient_fingerprint
			  
			RETURNING c.id,c.provider_event_id,c.event_type,c.recipient_fingerprint,c.bounce_type,c.rejection_reason,c.rejection_party
		),
		suppressions AS (
			INSERT INTO testagram_mail.recipient_suppressions(recipient_fingerprint,reason,source_event_id)
			SELECT recipient_fingerprint,
			       CASE
			         WHEN event_type='cf.email.sending.message.complained' THEN 'complaint'
			         WHEN event_type='cf.email.sending.message.rejected' THEN 'rejected'
			         ELSE 'hard_bounce'
			       END,
			       provider_event_id
			FROM matched
			WHERE event_type='cf.email.sending.message.complained'
			   OR (event_type='cf.email.sending.message.rejected' AND (rejection_party='recipient' OR rejection_reason='spam'))
			   OR (event_type='cf.email.sending.message.bounced' AND bounce_type='hard')
			ON CONFLICT(recipient_fingerprint) DO NOTHING
			RETURNING recipient_fingerprint
		)
		UPDATE testagram_mail.delivery_events e
		SET processed_at=now()
		WHERE e.id IN (SELECT id FROM matched)
	`)
	return err
}
