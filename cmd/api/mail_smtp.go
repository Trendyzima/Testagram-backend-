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
	"sort"
	"strings"
	"time"
)

const nativeMailFrom = "no-reply@testagram.site"

func mailProviderConfigurationError() error {
	// The name is retained for compatibility with existing readiness callers.
	// Native delivery has no provider account, API key, SMTP username, or password.
	_, err := mailCipher()
	return err
}

func sendNativeSMTP(ctx context.Context, message queuedMail) (string, error) {
	to, err := mail.ParseAddress(strings.TrimSpace(message.Recipient))
	if err != nil || to.Address == "" {
		return "", permanentMailError{code: "MAIL_RECIPIENT_INVALID"}
	}
	if strings.ContainsAny(message.Subject, "\r\n") || strings.TrimSpace(message.Subject) == "" || len(message.Subject) > 180 {
		return "", permanentMailError{code: "MAIL_SUBJECT_INVALID"}
	}
	if len(message.HTML) == 0 || len(message.HTML) > 256*1024 || len(message.Text) > 256*1024 {
		return "", permanentMailError{code: "MAIL_CONTENT_INVALID"}
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	messageID := hex.EncodeToString(nonce[:]) + "@testagram.site"
	payload, err := buildNativeSMTPMessage(to, message, messageID)
	if err != nil {
		return "", err
	}

	domain := to.Address[strings.LastIndex(to.Address, "@")+1:]
	if domain == "" {
		return "", permanentMailError{code: "MAIL_RECIPIENT_INVALID"}
	}
	mxs, lookupErr := net.DefaultResolver.LookupMX(ctx, domain)
	if lookupErr != nil || len(mxs) == 0 {
		// RFC 5321 permits an implicit MX when no MX record exists.
		mxs = []*net.MX{{Host: domain, Pref: 0}}
	}
	sort.Slice(mxs, func(i, j int) bool { return mxs[i].Pref < mxs[j].Pref })

	var lastErr error
	for _, mx := range mxs {
		host := strings.TrimSuffix(mx.Host, ".")
		if host == "" {
			continue
		}
		id, sendErr := sendNativeSMTPToMX(ctx, host, to.Address, payload)
		if sendErr == nil {
			return id, nil
		}
		var permanent permanentMailError
		if errors.As(sendErr, &permanent) {
			return "", sendErr
		}
		var ambiguous ambiguousMailError
		if errors.As(sendErr, &ambiguous) {
			return "", sendErr
		}
		lastErr = sendErr
	}
	if lastErr == nil {
		lastErr = errors.New("recipient domain has no usable mail exchanger")
	}
	return "", fmt.Errorf("native mail delivery could not reach recipient MX: %w", lastErr)
}

func sendNativeSMTPToMX(ctx context.Context, host, recipient string, payload []byte) (string, error) {
	dialer := &net.Dialer{Timeout: 12 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, "25"))
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(25 * time.Second))
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return "", fmt.Errorf("recipient MX SMTP greeting failed: %w", err)
	}
	defer client.Close()
	if ok, _ := client.Extension("STARTTLS"); !ok {
		return "", permanentMailError{code: "MAIL_MX_STARTTLS_UNAVAILABLE"}
	}
	if err := client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
		return "", fmt.Errorf("recipient MX STARTTLS failed: %w", err)
	}
	from, _ := mail.ParseAddress(nativeMailFrom)
	if err := client.Mail(from.Address); err != nil {
		return "", classifyNativeSMTPError("MAIL_MX_SENDER_REJECTED", err)
	}
	if err := client.Rcpt(recipient); err != nil {
		return "", classifyNativeSMTPError("MAIL_MX_RECIPIENT_REJECTED", err)
	}
	writer, err := client.Data()
	if err != nil {
		return "", classifyNativeSMTPError("MAIL_MX_DATA_REJECTED", err)
	}
	if _, err := writer.Write(payload); err != nil {
		_ = writer.Close()
		return "", ambiguousMailError{cause: err}
	}
	if err := writer.Close(); err != nil {
		var smtpErr *textproto.Error
		if errors.As(err, &smtpErr) {
			if smtpErr.Code >= 500 {
				return "", permanentMailError{code: "MAIL_MX_DATA_REJECTED"}
			}
			if smtpErr.Code >= 400 {
				return "", fmt.Errorf("recipient MX temporarily rejected message (%d): %w", smtpErr.Code, err)
			}
		}
		return "", ambiguousMailError{cause: err}
	}
	_ = client.Quit()
	return messageIDFromPayload(payload), nil
}

func classifyNativeSMTPError(code string, err error) error {
	var smtpErr *textproto.Error
	if errors.As(err, &smtpErr) {
		if smtpErr.Code >= 500 {
			return permanentMailError{code: code}
		}
		if smtpErr.Code >= 400 {
			return fmt.Errorf("recipient MX temporary SMTP rejection (%d): %w", smtpErr.Code, err)
		}
	}
	return fmt.Errorf("recipient MX SMTP command failed: %w", err)
}

func buildNativeSMTPMessage(to *mail.Address, message queuedMail, messageID string) ([]byte, error) {
	var out bytes.Buffer
	boundary := "testagram-" + strings.ReplaceAll(messageID, "@", "-")
	headers := []string{
		"From: Testagram <" + nativeMailFrom + ">",
		"To: " + to.String(),
		"Subject: " + mime.QEncoding.Encode("UTF-8", message.Subject),
		"Date: " + time.Now().UTC().Format(time.RFC1123Z),
		"Message-ID: <" + messageID + ">",
		"MIME-Version: 1.0",
		"Content-Type: multipart/alternative; boundary=\"" + boundary + "\"",
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
		out.WriteString(header + "\r\n")
	}
	out.WriteString("\r\n")
	if message.Text != "" {
		out.WriteString("--" + boundary + "\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
		qp := quotedprintable.NewWriter(&out)
		if _, err := qp.Write([]byte(message.Text)); err != nil { return nil, err }
		if err := qp.Close(); err != nil { return nil, err }
		out.WriteString("\r\n")
	}
	out.WriteString("--" + boundary + "\r\nContent-Type: text/html; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
	qp := quotedprintable.NewWriter(&out)
	if _, err := qp.Write([]byte(message.HTML)); err != nil { return nil, err }
	if err := qp.Close(); err != nil { return nil, err }
	out.WriteString("\r\n--" + boundary + "--\r\n")
	return out.Bytes(), nil
}

func messageIDFromPayload(payload []byte) string {
	for _, line := range strings.Split(string(payload), "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), "message-id: <") && strings.HasSuffix(line, ">") {
			return strings.TrimSuffix(strings.TrimPrefix(line, "Message-ID: <"), ">")
		}
	}
	return ""
}
