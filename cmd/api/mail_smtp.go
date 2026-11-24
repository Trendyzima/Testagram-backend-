package main

import (
	"bytes"
	"context"
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
)

const mailjetSendAPIURL = "https://api.mailjet.com/v3.1/send"

func mailProviderConfigurationError() error {
	apiKey := strings.TrimSpace(os.Getenv("MAILJET_API_KEY"))
	secret := strings.TrimSpace(os.Getenv("MAILJET_SECRET_KEY"))
	from := strings.TrimSpace(os.Getenv("AUTH_FROM_EMAIL"))
	if apiKey == "" || secret == "" || from == "" {
		return errors.New("Mailjet API is not configured")
	}
	if _, err := mail.ParseAddress(from); err != nil {
		return errors.New("AUTH_FROM_EMAIL is invalid")
	}
	if _, err := mailCipher(); err != nil {
		return err
	}
	return nil
}

type mailjetSendRequest struct {
	Messages []mailjetSendMessage `json:"Messages"`
}

type mailjetSendMessage struct {
	From     mailjetAddress    `json:"From"`
	To       []mailjetAddress  `json:"To"`
	Subject  string            `json:"Subject"`
	TextPart string            `json:"TextPart,omitempty"`
	HTMLPart string            `json:"HTMLPart,omitempty"`
	CustomID string            `json:"CustomID"`
	Headers  map[string]string `json:"Headers,omitempty"`
}

type mailjetAddress struct {
	Email string `json:"Email"`
	Name  string `json:"Name,omitempty"`
}

type mailjetSendResponse struct {
	Messages []struct {
		Status string `json:"Status"`
		Errors []struct {
			ErrorCode string `json:"ErrorCode"`
		} `json:"Errors"`
		To []struct {
			MessageID json.Number `json:"MessageID"`
		} `json:"To"`
	} `json:"Messages"`
}

func sendMailjetAPI(ctx context.Context, message queuedMail) (string, error) {
	client := &http.Client{Timeout: 25 * time.Second}
	return sendMailjetAPIWithClient(ctx, message, client, mailjetSendAPIURL)
}

// sendMailjetAPIWithClient is split out so transport and ambiguous-outcome
// handling can be tested without contacting Mailjet or sending real mail.
func sendMailjetAPIWithClient(ctx context.Context, message queuedMail, client *http.Client, endpoint string) (string, error) {
	apiKey := strings.TrimSpace(os.Getenv("MAILJET_API_KEY"))
	secret := strings.TrimSpace(os.Getenv("MAILJET_SECRET_KEY"))
	fromValue := strings.TrimSpace(os.Getenv("AUTH_FROM_EMAIL"))
	if apiKey == "" || secret == "" || fromValue == "" {
		return "", errors.New("Mailjet API is not configured")
	}

	from, err := mail.ParseAddress(fromValue)
	if err != nil {
		return "", permanentMailError{code: "MAIL_SENDER_INVALID"}
	}
	to, err := mail.ParseAddress(message.Recipient)
	if err != nil {
		return "", permanentMailError{code: "MAIL_RECIPIENT_INVALID"}
	}
	if strings.ContainsAny(message.Subject, "\r\n") || len(message.Subject) == 0 || len(message.Subject) > 180 {
		return "", permanentMailError{code: "MAIL_SUBJECT_INVALID"}
	}
	if len(message.HTML) == 0 || len(message.HTML) > 256*1024 || len(message.Text) > 256*1024 {
		return "", permanentMailError{code: "MAIL_CONTENT_INVALID"}
	}

	var randomID [16]byte
	if _, err := rand.Read(randomID[:]); err != nil {
		return "", err
	}
	customID := "testagram-" + hex.EncodeToString(randomID[:])

	headers := make(map[string]string, len(message.Headers))
	for key, value := range message.Headers {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "list-unsubscribe":
			key = "List-Unsubscribe"
		case "list-unsubscribe-post":
			key = "List-Unsubscribe-Post"
		default:
			return "", errors.New("unsupported custom mail header")
		}
		if strings.ContainsAny(key, "\r\n:") || strings.ContainsAny(value, "\r\n") {
			return "", errors.New("unsafe custom mail header")
		}
		headers[key] = value
	}

	payload, err := json.Marshal(mailjetSendRequest{Messages: []mailjetSendMessage{{
		From:     mailjetAddress{Email: from.Address, Name: "Testagram"},
		To:       []mailjetAddress{{Email: to.Address}},
		Subject:  message.Subject,
		TextPart: message.Text,
		HTMLPart: message.HTML,
		CustomID: customID,
		Headers:  headers,
	}}})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(apiKey, secret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		// The request may have reached Mailjet even if the response was lost.
		// Quarantine it rather than risk sending a duplicate security email.
		return "", ambiguousMailError{cause: err}
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return "", ambiguousMailError{cause: readErr}
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		// A 429 response is an explicit rejection, so the existing bounded
		// retry policy can safely schedule another attempt.
		return "", fmt.Errorf("Mailjet API rate limited the request")
	}
	if resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode >= 500 {
		return "", ambiguousMailError{cause: fmt.Errorf("Mailjet API returned HTTP %d", resp.StatusCode)}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", permanentMailError{code: "MAILJET_API_REJECTED"}
	}

	var result mailjetSendResponse
	if err := json.Unmarshal(body, &result); err != nil || len(result.Messages) != 1 {
		return "", ambiguousMailError{cause: errors.New("Mailjet API returned an invalid success response")}
	}
	resultMessage := result.Messages[0]
	if !strings.EqualFold(resultMessage.Status, "success") {
		return "", permanentMailError{code: "MAILJET_MESSAGE_REJECTED"}
	}
	if len(resultMessage.To) != 1 || strings.TrimSpace(resultMessage.To[0].MessageID.String()) == "" ||
		resultMessage.To[0].MessageID.String() == "0" {
		return "", ambiguousMailError{cause: errors.New("Mailjet API success response omitted the message identifier")}
	}

	// Mailjet returns this CustomID in its Event API payload. The outbox stores
	// it in provider_message_id so the existing event reconciliation matches
	// the exact queued message and recipient fingerprint.
	return customID, nil
}
