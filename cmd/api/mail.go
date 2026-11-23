package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	htmlstd "html"
	"io"
	"log"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"regexp"
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
	Text      string `json:"text,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
}

var mailHTMLTagPattern = regexp.MustCompile(`(?s)<[^>]*>`)
var mailAnchorPattern = regexp.MustCompile(`(?is)<a\\b[^>]*href=["\x27]([^"\x27]+)["\x27][^>]*>(.*?)</a>`)

func mailPlainTextFromHTML(value string) string {
	value = mailAnchorPattern.ReplaceAllStringFunc(value, func(anchor string) string {
		parts := mailAnchorPattern.FindStringSubmatch(anchor)
		if len(parts) != 3 { return anchor }
		label := strings.TrimSpace(mailHTMLTagPattern.ReplaceAllString(parts[2], ""))
		return label + " (" + htmlstd.UnescapeString(parts[1]) + ")"
	})
	value = strings.NewReplacer(
		"<br>", "\n", "<br/>", "\n", "<br />", "\n",
		"</p>", "\n", "</div>", "\n", "</li>", "\n", "</blockquote>", "\n",
	).Replace(value)
	value = mailHTMLTagPattern.ReplaceAllString(value, "")
	return strings.TrimSpace(htmlstd.UnescapeString(value))
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

func mailRuntimeConfigurationError() error {
	accountID := strings.TrimSpace(os.Getenv("CLOUDFLARE_EMAIL_ACCOUNT_ID"))
	apiToken := strings.TrimSpace(os.Getenv("CLOUDFLARE_EMAIL_API_TOKEN"))
	from := strings.TrimSpace(os.Getenv("AUTH_FROM_EMAIL"))
	if accountID == "" || apiToken == "" || from == "" {
		return errors.New("Cloudflare Email Sending configuration is incomplete")
	}
	decoded, err := hex.DecodeString(accountID)
	if err != nil || len(decoded) != 16 {
		return errors.New("Cloudflare Email Sending account ID is invalid")
	}
	if _, err := mail.ParseAddress(from); err != nil {
		return errors.New("Cloudflare Email Sending sender address is invalid")
	}
	if _, err := mailCipher(); err != nil {
		return err
	}
	return nil
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
	var headers map[string]string
	if preferenceUserID != nil {
		_, unsubscribeURL, err := createSocialUnsubscribeToken(ctx, executor, *preferenceUserID, preferenceCategory)
		if err != nil { return err }
		html += `<p style="margin-top:24px;color:#667085;font-size:13px">You can <a href="` + htmlstd.EscapeString(unsubscribeURL) + `">unsubscribe from this type of email</a> at any time.</p>`
		headers = map[string]string{
			"List-Unsubscribe": "<" + unsubscribeURL + ">",
			"List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
		}
	}
	html = brandedTestagramMailHTML(subject, html)
	aead, err := mailCipher()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(queuedMail{Recipient: recipient, Subject: subject, HTML: html, Text: mailPlainTextFromHTML(html), Headers: headers})
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
	fingerprint, err := mailRecipientFingerprint(recipient)
	if err != nil {
		return err
	}
	if preferenceUserID == nil {
		_, err = executor.Exec(ctx, `
			INSERT INTO testagram_mail.outbox(payload_ciphertext,payload_nonce,status,available_at,recipient_fingerprint)
			VALUES($1,$2,'pending',now(),$3)
		`, ciphertext, nonce, fingerprint)
		return err
	}
	_, err = executor.Exec(ctx, `
		INSERT INTO testagram_mail.outbox(
			payload_ciphertext,payload_nonce,status,available_at,preference_user_id,preference_category,recipient_fingerprint
		)
		VALUES($1,$2,'pending',now(),$3,$4,$5)
	`, ciphertext, nonce, preferenceID, category, fingerprint)
	return err
}


func brandedTestagramMailHTML(subject, content string) string {
	baseURL := strings.TrimRight(env("AUTH_PUBLIC_BASE_URL", "https://testagram.site"), "/")
	brandURL := htmlstd.EscapeString(baseURL)
	preheader := htmlstd.EscapeString(strings.TrimSpace(subject))
	return `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="color-scheme" content="light"><title>` + preheader + `</title></head><body style="margin:0;padding:24px 12px;background:#f3f5f8;color:#182230;font-family:Arial,Helvetica,sans-serif;line-height:1.55"><span style="display:none!important;visibility:hidden;opacity:0;height:0;width:0;overflow:hidden">` + preheader + `</span><table role="presentation" cellpadding="0" cellspacing="0" border="0" width="100%" style="max-width:600px;margin:0 auto;background:#ffffff;border:1px solid #e4e8ee;border-radius:12px"><tr><td style="padding:24px 28px 12px"><a href="` + brandURL + `" style="color:#155eef;text-decoration:none;font-size:22px;font-weight:700">Testagram</a></td></tr><tr><td style="padding:12px 28px 28px;font-size:16px">` + content + `</td></tr><tr><td style="padding:18px 28px;border-top:1px solid #e4e8ee;color:#667085;font-size:12px">You received this message from Testagram. <a href="` + brandURL + `" style="color:#155eef">Visit Testagram</a>.</td></tr></table></body></html>`
}

func createSocialUnsubscribeToken(ctx context.Context, executor mailQueueExecutor, userID, category string) (string, string, error) {
	if executor == nil || strings.TrimSpace(userID) == "" ||
		(category != "new_follower" && category != "new_post") {
		return "", "", errors.New("invalid social unsubscribe token request")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil { return "", "", err }
	token := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256(raw)
	baseURL := strings.TrimRight(env("AUTH_PUBLIC_BASE_URL", "https://testagram.site"), "/")
	unsubscribeURL := baseURL + "/api/email/unsubscribe?token=" + url.QueryEscape(token)
	_, err := executor.Exec(ctx, `
		INSERT INTO testagram_mail.social_unsubscribe_tokens(token_hash,user_id,preference_category,expires_at)
		VALUES($1,$2::uuid,$3,now()+interval '365 days')
	`, hash[:], userID, category)
	if err != nil { return "", "", err }
	return token, unsubscribeURL, nil
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
	payload := map[string]any{
		"to": message.Recipient, "from": from, "subject": message.Subject, "html": message.HTML,
	}
	if len(message.Headers) > 0 { payload["headers"] = message.Headers }
	if strings.TrimSpace(message.Text) != "" {
		payload["text"] = message.Text
	}
	body, err := json.Marshal(payload)
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
	if err := mailRuntimeConfigurationError(); err != nil {
		log.Printf("mail outbox worker is idle: %v", err)
	}
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
	if err := mailRuntimeConfigurationError(); err != nil {
		return false, nil
	}
	if err := s.reconcileCloudflareMailEvents(ctx); err != nil {
		return false, err
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
	fingerprint, err := mailRecipientFingerprint(message.Recipient)
	if err != nil {
		_ = s.failMail(ctx, id, attempt, "MAIL_RECIPIENT_FINGERPRINT_FAILED")
		return true, err
	}
	var suppressed bool
	if err := s.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM testagram_mail.recipient_suppressions
			WHERE recipient_fingerprint=$1
		)
	`, fingerprint).Scan(&suppressed); err != nil {
		_ = s.failMail(ctx, id, attempt, "MAIL_SUPPRESSION_LOOKUP_FAILED")
		return true, err
	}
	if suppressed {
		_, err := s.db.Exec(ctx, `
			UPDATE testagram_mail.outbox
			SET status='dead',lease_until=NULL,last_error_code='RECIPIENT_SUPPRESSED',
			    payload_ciphertext='',payload_nonce='',updated_at=now()
			WHERE id=$1 AND status='sending'
		`, id)
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
