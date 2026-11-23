package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/mail"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	mailWorkerInterval = 5 * time.Second
	mailMaxAttempts    = 12
)

type permanentMailError struct{}

func (permanentMailError) Error() string {
	return "recipient was permanently bounced or suppressed"
}

type ambiguousMailError struct {
	cause error
}

func (e ambiguousMailError) Error() string {
	return "Cloudflare delivery outcome is unknown; manual reconciliation is required"
}

func (e ambiguousMailError) Unwrap() error { return e.cause }

type queuedMail struct {
	Recipient string `json:"recipient"`
	Subject   string `json:"subject"`
	HTML      string `json:"html"`
}

type cloudflareMailResponse struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Result struct {
		MessageID        string   `json:"message_id"`
		Delivered        []string `json:"delivered"`
		Queued           []string `json:"queued"`
		PermanentBounces []string `json:"permanent_bounces"`
		Suppressed       []string `json:"suppressed_recipients"`
	} `json:"result"`
}

func mailCipher() (cipher.AEAD, error) {
	raw, err := hex.DecodeString(strings.TrimSpace(os.Getenv("TESTAGRAM_MAIL_ENCRYPTION_KEY")))
	if err != nil || len(raw) != 32 {
		return nil, errors.New("TESTAGRAM_MAIL_ENCRYPTION_KEY must be 64 hexadecimal characters")
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

type mailQueueExecutor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func enqueueAuthEmailWith(ctx context.Context, executor mailQueueExecutor, recipient, subject, html string) error {
	return enqueueMailWithPreference(ctx, executor, recipient, subject, html, nil, "")
}

func enqueueSocialEmailWith(ctx context.Context, executor mailQueueExecutor, recipient, recipientUserID, category, subject, html string) error {
	if strings.TrimSpace(recipientUserID) == "" ||
		(category != "new_follower" && category != "new_post") {
		return errors.New("invalid social email consent reference")
	}
	return enqueueMailWithPreference(ctx, executor, recipient, subject, html, &recipientUserID, category)
}

func enqueueMailWithPreference(ctx context.Context, executor mailQueueExecutor, recipient, subject, html string, preferenceUserID *string, preferenceCategory string) error {
	if executor == nil {
		return errors.New("mail queue database unavailable")
	}
	if _, err := mail.ParseAddress(recipient); err != nil {
		return errors.New("invalid mail recipient")
	}
	if len(subject) == 0 || len(subject) > 180 || strings.ContainsAny(subject, "\r\n") || len(html) > 256*1024 {
		return errors.New("mail content exceeds allowed bounds")
	}
	var preferenceID any
	var category any
	if preferenceUserID != nil {
		if strings.TrimSpace(*preferenceUserID) == "" ||
			(preferenceCategory != "new_follower" && preferenceCategory != "new_post") {
			return errors.New("invalid social email consent reference")
		}
		preferenceID = *preferenceUserID
		category = preferenceCategory
	}
	aead, err := mailCipher()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(queuedMail{Recipient: recipient, Subject: subject, HTML: html})
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	ciphertext := aead.Seal(nil, nonce, payload, []byte("testagram-mail-v1"))
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if preferenceUserID == nil {
		_, err = executor.Exec(ctx, `
			INSERT INTO testagram_mail.outbox(payload_ciphertext,payload_nonce,status,available_at)
			VALUES($1,$2,'pending',now())
		`, ciphertext, nonce)
		return err
	}
	_, err = executor.Exec(ctx, `
		INSERT INTO testagram_mail.outbox(
			payload_ciphertext,payload_nonce,status,available_at,preference_user_id,preference_category
		)
		VALUES($1,$2,'pending',now(),$3,$4)
	`, ciphertext, nonce, preferenceID, category)
	return err
}

func (s *service) enqueueAuthEmail(ctx context.Context, recipient, subject, html string) error {
	return enqueueAuthEmailWith(ctx, s.db, recipient, subject, html)
}

func (s *service) sendCloudflareMail(ctx context.Context, message queuedMail) (string, error) {
	accountID := strings.TrimSpace(os.Getenv("CLOUDFLARE_EMAIL_ACCOUNT_ID"))
	apiToken := strings.TrimSpace(os.Getenv("CLOUDFLARE_EMAIL_API_TOKEN"))
	from := strings.TrimSpace(os.Getenv("AUTH_FROM_EMAIL"))
	if accountID == "" || apiToken == "" || from == "" {
		return "", errors.New("Cloudflare Email Sending is not configured")
	}
	if _, err := mail.ParseAddress(from); err != nil {
		return "", errors.New("AUTH_FROM_EMAIL is invalid")
	}
	decoded, decodeErr := hex.DecodeString(accountID)
	if decodeErr != nil || len(decoded) != 16 {
		return "", errors.New("CLOUDFLARE_EMAIL_ACCOUNT_ID must be a 32-character hexadecimal account ID")
	}
	endpoint := "https://api.cloudflare.com/client/v4/accounts/" + accountID + "/email/sending/send"
	client := &http.Client{
		Timeout: 20 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return sendCloudflareMailRequest(ctx, client, endpoint, apiToken, from, message)
}

func sendCloudflareMailRequest(ctx context.Context, client *http.Client, endpoint, apiToken, from string, message queuedMail) (string, error) {
	if client == nil {
		return "", errors.New("Cloudflare Email Sending HTTP client unavailable")
	}
	body, err := json.Marshal(map[string]string{
		"to": []string{message.Recipient}, "from": from, "subject": message.Subject, "html": message.HTML,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", ambiguousMailError{cause: err}
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if readErr != nil {
		return "", ambiguousMailError{cause: readErr}
	}
	var result cloudflareMailResponse
	if err := json.Unmarshal(data, &result); err != nil {
		if resp.StatusCode >= 200 && resp.StatusCode < 300 || resp.StatusCode >= 500 {
			return "", ambiguousMailError{cause: fmt.Errorf("unreadable provider response (HTTP %d)", resp.StatusCode)}
		}
		return "", fmt.Errorf("Cloudflare Email Sending returned an unreadable rejection (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode >= 500 {
		return "", ambiguousMailError{cause: fmt.Errorf("provider server error (HTTP %d)", resp.StatusCode)}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !result.Success {
		if len(result.Errors) > 0 {
			return "", fmt.Errorf("Cloudflare Email Sending rejected the request (HTTP %d, code %d)", resp.StatusCode, result.Errors[0].Code)
		}
		return "", fmt.Errorf("Cloudflare Email Sending rejected the request (HTTP %d)", resp.StatusCode)
	}
	if strings.TrimSpace(result.Result.MessageID) == "" {
		return "", ambiguousMailError{cause: errors.New("provider accepted the request without returning a message ID")}
	}
	if len(result.Result.PermanentBounces) > 0 || len(result.Result.Suppressed) > 0 {
		return result.Result.MessageID, permanentMailError{}
	}
	if len(result.Result.Delivered) == 0 && len(result.Result.Queued) == 0 {
		return result.Result.MessageID, ambiguousMailError{cause: errors.New("provider returned no recipient outcome")}
	}
	return result.Result.MessageID, nil
}

func (s *service) runMailOutboxWorker(ctx context.Context) {
	ticker := time.NewTicker(mailWorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for i := 0; i < 20; i++ {
				if ctx.Err() != nil {
					return
				}
				worked, err := s.processOneMail(ctx)
				if err != nil {
					log.Printf("mail outbox worker batch failed; retrying on next tick")
					break
				}
				if !worked {
					break
				}
			}
		}
	}
}

type mailConsentQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func socialMailConsentActive(ctx context.Context, queryer mailConsentQueryer, userID, category string) (bool, error) {
	if userID == "" || (category != "new_follower" && category != "new_post") {
		return false, nil
	}
	var active bool
	err := queryer.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM testagram_local.email_notification_preferences p
			WHERE p.user_id=$1::uuid AND (
				($2='new_follower' AND p.new_follower_email_enabled=TRUE)
				OR ($2='new_post' AND p.new_post_email_enabled=TRUE)
			)
		)
	`, userID, category).Scan(&active)
	return active, err
}

func (s *service) processOneMail(ctx context.Context) (bool, error) {
	if s.db == nil {
		return false, errors.New("mail database unavailable")
	}
	if strings.TrimSpace(os.Getenv("CLOUDFLARE_EMAIL_ACCOUNT_ID")) == "" ||
		strings.TrimSpace(os.Getenv("CLOUDFLARE_EMAIL_API_TOKEN")) == "" ||
		strings.TrimSpace(os.Getenv("AUTH_FROM_EMAIL")) == "" {
		return false, nil
	}
	if _, err := mailCipher(); err != nil {
		return false, nil
	}

	// Remove social messages whose consent is absent or has been withdrawn.
	// This also handles deleted accounts and stale queue rows from older builds.
	if _, err := s.db.Exec(ctx, `
		UPDATE testagram_mail.outbox o
		SET status='dead',lease_until=NULL,last_error_code='CONSENT_WITHDRAWN',
		    payload_ciphertext='',payload_nonce='',updated_at=now()
		WHERE o.status='pending' AND o.preference_user_id IS NOT NULL
		  AND NOT EXISTS (
		    SELECT 1 FROM testagram_local.email_notification_preferences p
		    WHERE p.user_id=o.preference_user_id AND (
		      (o.preference_category='new_follower' AND p.new_follower_email_enabled=TRUE)
		      OR (o.preference_category='new_post' AND p.new_post_email_enabled=TRUE)
		    )
		  )
	`); err != nil {
		return false, err
	}

	// A worker may have crashed after Cloudflare accepted a request but before
	// recording the response. Never reclaim an expired sending lease: its
	// provider outcome is unknowable and automatic resend could duplicate mail.
	if _, err := s.db.Exec(ctx, `
		UPDATE testagram_mail.outbox
		SET status='uncertain',lease_until=NULL,last_error_code='MAIL_LEASE_EXPIRED_OUTCOME_UNKNOWN',updated_at=now()
		WHERE status='sending' AND lease_until<now()
	`); err != nil {
		return false, err
	}

	var id int64
	var ciphertext, nonce []byte
	var attempt int
	var preferenceUserID, preferenceCategory string
	err := s.db.QueryRow(ctx, `
		WITH candidate AS (
			SELECT id FROM testagram_mail.outbox o
			WHERE o.status='pending' AND o.available_at<=now()
			  AND (o.preference_user_id IS NULL OR EXISTS (
			    SELECT 1 FROM testagram_local.email_notification_preferences p
			    WHERE p.user_id=o.preference_user_id AND (
			      (o.preference_category='new_follower' AND p.new_follower_email_enabled=TRUE)
			      OR (o.preference_category='new_post' AND p.new_post_email_enabled=TRUE)
			    )
			  ))
			ORDER BY o.available_at,o.id
			LIMIT 1 FOR UPDATE SKIP LOCKED
		)
		UPDATE testagram_mail.outbox AS o
		SET status='sending',attempts=o.attempts+1,lease_until=now()+interval '2 minutes',updated_at=now()
		FROM candidate WHERE o.id=candidate.id
		RETURNING o.id,o.payload_ciphertext,o.payload_nonce,o.attempts,
		          COALESCE(o.preference_user_id::text,''),COALESCE(o.preference_category,'')
	`).Scan(&id, &ciphertext, &nonce, &attempt, &preferenceUserID, &preferenceCategory)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	aead, err := mailCipher()
	if err != nil {
		_ = s.failMail(ctx, id, attempt, "MAIL_ENCRYPTION_NOT_CONFIGURED")
		return true, err
	}
	plain, err := aead.Open(nil, nonce, ciphertext, []byte("testagram-mail-v1"))
	if err != nil {
		_ = s.failMail(ctx, id, attempt, "MAIL_PAYLOAD_DECRYPT_FAILED")
		return true, err
	}
	var message queuedMail
	if err := json.Unmarshal(plain, &message); err != nil {
		_ = s.failMail(ctx, id, attempt, "MAIL_PAYLOAD_INVALID")
		return true, err
	}
	// Re-check consent immediately before the provider call so queued social
	// messages cannot be sent after a preference withdrawal.
	if preferenceUserID != "" {
		consentActive, err := socialMailConsentActive(ctx, s.db, preferenceUserID, preferenceCategory)
		if err != nil {
			_ = s.failMail(ctx, id, attempt, "SOCIAL_EMAIL_CONSENT_LOOKUP_FAILED")
			return true, err
		}
		if !consentActive {
			_, err := s.db.Exec(ctx, `UPDATE testagram_mail.outbox
				SET status='dead',lease_until=NULL,last_error_code='CONSENT_WITHDRAWN',
				    payload_ciphertext='',payload_nonce='',updated_at=now()
				WHERE id=$1 AND status='sending'`, id)
			return true, err
		}
	}

	sendCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	messageID, sendErr := s.sendCloudflareMail(sendCtx, message)
	cancel()
	if sendErr != nil {
		var ambiguous ambiguousMailError
		if errors.As(sendErr, &ambiguous) {
			_, err := s.db.Exec(ctx, `
				UPDATE testagram_mail.outbox
				SET status='uncertain',lease_until=NULL,last_error_code='CLOUDFLARE_OUTCOME_UNKNOWN',updated_at=now()
				WHERE id=$1 AND status='sending'
			`, id)
			return true, err
		}
		var permanent permanentMailError
		if errors.As(sendErr, &permanent) {
			_, err := s.db.Exec(ctx, `
				UPDATE testagram_mail.outbox
				SET status='dead',lease_until=NULL,provider_message_id=NULLIF($2,''),
				    last_error_code='CLOUDFLARE_PERMANENT_RECIPIENT_FAILURE',payload_ciphertext='',payload_nonce='',updated_at=now()
				WHERE id=$1 AND status='sending'
			`, id, messageID)
			return true, err
		}
		code := "CLOUDFLARE_DELIVERY_FAILED"
		if strings.Contains(sendErr.Error(), "not configured") {
			code = "CLOUDFLARE_NOT_CONFIGURED"
		}
		if err := s.failMail(ctx, id, attempt, code); err != nil {
			return true, err
		}
		return true, nil
	}
	_, err = s.db.Exec(ctx, `
		UPDATE testagram_mail.outbox
		SET status='sent',sent_at=now(),lease_until=NULL,provider_message_id=NULLIF($2,''),last_error_code=NULL,
		    payload_ciphertext='',payload_nonce='',updated_at=now()
		WHERE id=$1 AND status='sending'
	`, id, messageID)
	return true, err
}

func (s *service) failMail(ctx context.Context, id int64, attempt int, code string) error {
	if attempt >= mailMaxAttempts {
		_, err := s.db.Exec(ctx, `
			UPDATE testagram_mail.outbox
			SET status='dead',lease_until=NULL,last_error_code=$2,payload_ciphertext='',payload_nonce='',updated_at=now()
			WHERE id=$1
		`, id, code)
		return err
	}
	delay := time.Duration(1<<min(attempt-1, 8)) * time.Minute
	_, err := s.db.Exec(ctx, `
		UPDATE testagram_mail.outbox
		SET status='pending',available_at=now()+($2 * interval '1 second'),
		    lease_until=NULL,last_error_code=$3,updated_at=now()
		WHERE id=$1
	`, id, int64(delay.Seconds()), code)
	return err
}
