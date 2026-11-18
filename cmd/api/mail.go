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
	"net/http"
	"net/mail"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	mailWorkerInterval = 5 * time.Second
	mailMaxAttempts = 12
)

type queuedMail struct {
	Recipient string `json:"recipient"`
	Subject string `json:"subject"`
	HTML string `json:"html"`
}

type cloudflareMailResponse struct {
	Success bool `json:"success"`
	Errors []struct { Code int `json:"code"`; Message string `json:"message"` } `json:"errors"`
	Result struct {
		MessageID string `json:"message_id"`
		Delivered []string `json:"delivered"`
		Queued []string `json:"queued"`
		PermanentBounces []string `json:"permanent_bounces"`
		Suppressed []string `json:"suppressed_recipients"`
	} `json:"result"`
}

func mailCipher() (cipher.AEAD, error) {
	raw, err := hex.DecodeString(strings.TrimSpace(os.Getenv("TESTAGRAM_MAIL_ENCRYPTION_KEY")))
	if err != nil || len(raw) != 32 {
		return nil, errors.New("TESTAGRAM_MAIL_ENCRYPTION_KEY must be 64 hexadecimal characters")
	}
	block, err := aes.NewCipher(raw)
	if err != nil { return nil, err }
	return cipher.NewGCM(block)
}

func (s *service) enqueueAuthEmail(ctx context.Context, recipient, subject, html string) error {
	if s.db == nil { return errors.New("mail queue database unavailable") }
	if _, err := mail.ParseAddress(recipient); err != nil { return errors.New("invalid mail recipient") }
	if len(subject) == 0 || len(subject) > 180 || strings.ContainsAny(subject, "\r\n") || len(html) > 256*1024 {
		return errors.New("mail content exceeds allowed bounds")
	}
	aead, err := mailCipher()
	if err != nil { return err }
	payload, err := json.Marshal(queuedMail{Recipient: recipient, Subject: subject, HTML: html})
	if err != nil { return err }
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil { return err }
	ciphertext := aead.Seal(nil, nonce, payload, []byte("testagram-mail-v1"))
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err = s.db.Exec(ctx, `
		INSERT INTO testagram_mail.outbox(payload_ciphertext,payload_nonce,status,available_at)
		VALUES($1,$2,'pending',now())
	`, ciphertext, nonce)
	return err
}

func (s *service) sendCloudflareMail(ctx context.Context, message queuedMail) (string, error) {
	accountID := strings.TrimSpace(os.Getenv("CLOUDFLARE_EMAIL_ACCOUNT_ID"))
	apiToken := strings.TrimSpace(os.Getenv("CLOUDFLARE_EMAIL_API_TOKEN"))
	from := strings.TrimSpace(os.Getenv("AUTH_FROM_EMAIL"))
	if accountID == "" || apiToken == "" || from == "" {
		return "", errors.New("Cloudflare Email Sending is not configured")
	}
	if _, err := mail.ParseAddress(from); err != nil { return "", errors.New("AUTH_FROM_EMAIL is invalid") }
	body, err := json.Marshal(map[string]string{
		"to": message.Recipient, "from": from, "subject": message.Subject, "html": message.HTML,
	})
	if err != nil { return "", err }
	endpoint := "https://api.cloudflare.com/client/v4/accounts/" + accountID + "/email/sending/send"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil { return "", err }
	req.Header.Set("Authorization", "Bearer "+apiToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 20*time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil { return "", err }
	defer resp.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if readErr != nil { return "", readErr }
	var result cloudflareMailResponse
	if err := json.Unmarshal(data, &result); err != nil {
		return "", fmt.Errorf("Cloudflare Email Sending returned an unreadable response (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !result.Success {
		if len(result.Errors) > 0 { return "", fmt.Errorf("Cloudflare Email Sending rejected the request (HTTP %d, code %d)", resp.StatusCode, result.Errors[0].Code) }
		return "", fmt.Errorf("Cloudflare Email Sending rejected the request (HTTP %d)", resp.StatusCode)
	}
	if len(result.Result.PermanentBounces) > 0 || len(result.Result.Suppressed) > 0 {
		return result.Result.MessageID, errors.New("recipient was permanently bounced or suppressed")
	}
	if len(result.Result.Delivered) == 0 && len(result.Result.Queued) == 0 {
		return result.Result.MessageID, errors.New("Cloudflare accepted no recipient for delivery")
	}
	return result.Result.MessageID, nil
}

func (s *service) runMailOutboxWorker(ctx context.Context) {
	ticker := time.NewTicker(mailWorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done(): return
		case <-ticker.C:
			for i := 0; i < 20; i++ {
				if ctx.Err() != nil { return }
				worked, err := s.processOneMail(ctx)
				if err != nil { log.Printf("mail outbox worker batch failed; retrying on next tick") ; break }
				if !worked { break }
			}
		}
	}
}

func (s *service) processOneMail(ctx context.Context) (bool, error) {
	if s.db == nil { return false, errors.New("mail database unavailable") }
	var id int64
	var ciphertext, nonce []byte
	var attempt int
	err := s.db.QueryRow(ctx, `
		WITH candidate AS (
			SELECT id FROM testagram_mail.outbox
			WHERE (status='pending' AND available_at<=now())
			   OR (status='sending' AND lease_until<now())
			ORDER BY available_at,id
			FOR UPDATE SKIP LOCKED LIMIT 1
		)
		UPDATE testagram_mail.outbox AS o
		SET status='sending',attempts=o.attempts+1,lease_until=now()+interval '2 minutes',updated_at=now()
		FROM candidate WHERE o.id=candidate.id
		RETURNING o.id,o.payload_ciphertext,o.payload_nonce,o.attempts
	`).Scan(&id,&ciphertext,&nonce,&attempt)
	if errors.Is(err, pgx.ErrNoRows) { return false,nil }
	if err != nil { return false,err }
	aead, err := mailCipher()
	if err != nil { _ = s.failMail(ctx,id,attempt,"MAIL_ENCRYPTION_NOT_CONFIGURED"); return true,err }
	plain, err := aead.Open(nil,nonce,ciphertext,[]byte("testagram-mail-v1"))
	if err != nil { _ = s.failMail(ctx,id,attempt,"MAIL_PAYLOAD_DECRYPT_FAILED"); return true,err }
	var message queuedMail
	if err := json.Unmarshal(plain,&message); err != nil { _ = s.failMail(ctx,id,attempt,"MAIL_PAYLOAD_INVALID"); return true,err }
	sendCtx,cancel := context.WithTimeout(ctx,25*time.Second)
	messageID, sendErr := s.sendCloudflareMail(sendCtx,message)
	cancel()
	if sendErr != nil {
		code := "CLOUDFLARE_DELIVERY_FAILED"
		if strings.Contains(sendErr.Error(),"not configured") { code = "CLOUDFLARE_NOT_CONFIGURED" }
		if err := s.failMail(ctx,id,attempt,code); err != nil { return true,err }
		return true,nil
	}
	_, err = s.db.Exec(ctx,`
		UPDATE testagram_mail.outbox
		SET status='sent',sent_at=now(),lease_until=NULL,provider_message_id=NULLIF($2,''),last_error_code=NULL,
		    payload_ciphertext='',payload_nonce='',updated_at=now()
		WHERE id=$1 AND status='sending'
	`,id,messageID)
	return true,err
}

func (s *service) failMail(ctx context.Context,id int64,attempt int,code string) error {
	if attempt >= mailMaxAttempts {
		_,err:=s.db.Exec(ctx,`UPDATE testagram_mail.outbox SET status='dead',lease_until=NULL,last_error_code=$2,
			payload_ciphertext='',payload_nonce='',updated_at=now() WHERE id=$1`,id,code)
		return err
	}
	delay:=time.Duration(1<<min(attempt-1,8))*time.Minute
	_,err:=s.db.Exec(ctx,`UPDATE testagram_mail.outbox SET status='pending',available_at=now()+$2::interval,
		lease_until=NULL,last_error_code=$3,updated_at=now() WHERE id=$1`,id,delay.String(),code)
	return err
}
