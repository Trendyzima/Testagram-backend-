package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"os"
	"strings"
	"time"
)

const mailjetSMTPHost = "in-v3.mailjet.com"

func mailSMTPConfigurationError() error {
	username := strings.TrimSpace(os.Getenv("MAILJET_SMTP_USERNAME"))
	password := strings.TrimSpace(os.Getenv("MAILJET_SMTP_PASSWORD"))
	from := strings.TrimSpace(os.Getenv("AUTH_FROM_EMAIL"))
	if username == "" || password == "" || from == "" {
		return errors.New("Mailjet SMTP is not configured")
	}
	if _, err := mail.ParseAddress(from); err != nil {
		return errors.New("AUTH_FROM_EMAIL is invalid")
	}
	port := strings.TrimSpace(os.Getenv("MAILJET_SMTP_PORT"))
	if port == "" {
		port = "587"
	}
	if port != "587" {
		return errors.New("MAILJET_SMTP_PORT must be 587 (STARTTLS)")
	}
	if _, err := mailCipher(); err != nil {
		return err
	}
	return nil
}

func sendMailjetSMTP(ctx context.Context, message queuedMail) (string, error) {
	username := strings.TrimSpace(os.Getenv("MAILJET_SMTP_USERNAME"))
	password := strings.TrimSpace(os.Getenv("MAILJET_SMTP_PASSWORD"))
	fromValue := strings.TrimSpace(os.Getenv("AUTH_FROM_EMAIL"))
	port := strings.TrimSpace(os.Getenv("MAILJET_SMTP_PORT"))
	if port == "" {
		port = "587"
	}
	if username == "" || password == "" || fromValue == "" {
		return "", errors.New("Mailjet SMTP is not configured")
	}
	from, err := mail.ParseAddress(fromValue)
	if err != nil {
		return "", permanentMailError{code: "MAIL_SENDER_INVALID"}
	}
	to, err := mail.ParseAddress(message.Recipient)
	if err != nil {
		return "", permanentMailError{code: "MAIL_RECIPIENT_INVALID"}
	}
	if strings.ContainsAny(message.Subject, "\r\n") || len(message.Subject) > 180 {
		return "", permanentMailError{code: "MAIL_SUBJECT_INVALID"}
	}

	dialer := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(mailjetSMTPHost, port))
	if err != nil {
		return "", fmt.Errorf("Mailjet SMTP connection failed: %w", err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(25 * time.Second))
	}
	client, err := smtp.NewClient(conn, mailjetSMTPHost)
	if err != nil {
		return "", fmt.Errorf("Mailjet SMTP greeting failed: %w", err)
	}
	defer client.Close()

	if ok, _ := client.Extension("STARTTLS"); !ok {
		return "", permanentMailError{code: "MAIL_SMTP_STARTTLS_REQUIRED"}
	}
	if err := client.StartTLS(&tls.Config{ServerName: mailjetSMTPHost, MinVersion: tls.VersionTLS12}); err != nil {
		return "", fmt.Errorf("Mailjet SMTP STARTTLS failed: %w", err)
	}
	if err := client.Auth(smtp.PlainAuth("", username, password, mailjetSMTPHost)); err != nil {
		var smtpErr *textproto.Error
		if errors.As(err, &smtpErr) && smtpErr.Code >= 500 {
			return "", permanentMailError{code: "MAIL_SMTP_AUTH_REJECTED"}
		}
		return "", fmt.Errorf("Mailjet SMTP authentication failed: %w", err)
	}
	if err := client.Mail(from.Address); err != nil {
		return "", classifySMTPCommandError("MAIL_SMTP_SENDER_REJECTED", err)
	}
	if err := client.Rcpt(to.Address); err != nil {
		return "", classifySMTPCommandError("MAIL_SMTP_RECIPIENT_REJECTED", err)
	}

	var randomID [16]byte
	if _, err := rand.Read(randomID[:]); err != nil {
		return "", err
	}
	messageID := "testagram-" + hex.EncodeToString(randomID[:]) + "@testagram.site"
	payload, err := buildSMTPMessage(from, to, message, messageID)
	if err != nil {
		return "", err
	}
	writer, err := client.Data()
	if err != nil {
		return "", classifySMTPCommandError("MAIL_SMTP_DATA_REJECTED", err)
	}
	if _, err := writer.Write(payload); err != nil {
		// Once DATA starts, the remote server may have accepted a partial or
		// complete message. Quarantine instead of risking a duplicate send.
		return "", ambiguousMailError{cause: err}
	}
	if err := writer.Close(); err != nil {
		var smtpErr *textproto.Error
		if errors.As(err, &smtpErr) {
			if smtpErr.Code >= 500 {
				return "", permanentMailError{code: "MAIL_SMTP_DATA_REJECTED"}
			}
			if smtpErr.Code >= 400 {
				return "", fmt.Errorf("Mailjet SMTP temporarily rejected DATA (%d): %w", smtpErr.Code, err)
			}
		}
		return "", ambiguousMailError{cause: err}
	}
	_ = client.Quit()
	return messageID, nil
}

func classifySMTPCommandError(code string, err error) error {
	var smtpErr *textproto.Error
	if errors.As(err, &smtpErr) {
		if smtpErr.Code >= 500 {
			return permanentMailError{code: code}
		}
		if smtpErr.Code >= 400 {
			return fmt.Errorf("Mailjet SMTP temporary rejection (%d): %w", smtpErr.Code, err)
		}
	}
	return fmt.Errorf("Mailjet SMTP command failed: %w", err)
}

func buildSMTPMessage(from, to *mail.Address, message queuedMail, messageID string) ([]byte, error) {
	var out bytes.Buffer
	headers := []string{
		"X-MJ-CustomID: " + messageID,
		"From: " + from.String(),
		"To: " + to.String(),
		"Subject: " + mime.QEncoding.Encode("UTF-8", message.Subject),
		"Date: " + time.Now().UTC().Format(time.RFC1123Z),
		"Message-ID: <" + messageID + ">",
		"MIME-Version: 1.0",
	}
	for key, value := range message.Headers {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "list-unsubscribe", "list-unsubscribe-post":
		default:
			return nil, errors.New("unsupported custom mail header")
		}
		if strings.ContainsAny(key, "\r\n:") || strings.ContainsAny(value, "\r\n") {
			return nil, errors.New("unsafe custom mail header")
		}
		headers = append(headers, key+": "+value)
	}
	for _, header := range headers {
		out.WriteString(header)
		out.WriteString("\r\n")
	}
	if strings.TrimSpace(message.Text) == "" {
		out.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
		out.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
		if err := writeQuotedPrintable(&out, message.HTML); err != nil {
			return nil, err
		}
		return out.Bytes(), nil
	}
	boundaryBytes := make([]byte, 18)
	if _, err := rand.Read(boundaryBytes); err != nil {
		return nil, err
	}
	boundary := "testagram-" + hex.EncodeToString(boundaryBytes)
	out.WriteString("Content-Type: multipart/alternative; boundary=\"" + boundary + "\"\r\n\r\n")
	out.WriteString("--" + boundary + "\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
	if err := writeQuotedPrintable(&out, message.Text); err != nil {
		return nil, err
	}
	out.WriteString("\r\n--" + boundary + "\r\nContent-Type: text/html; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
	if err := writeQuotedPrintable(&out, message.HTML); err != nil {
		return nil, err
	}
	out.WriteString("\r\n--" + boundary + "--\r\n")
	return out.Bytes(), nil
}

func writeQuotedPrintable(out *bytes.Buffer, value string) error {
	writer := quotedprintable.NewWriter(out)
	if _, err := writer.Write([]byte(value)); err != nil {
		_ = writer.Close()
		return err
	}
	return writer.Close()
}

