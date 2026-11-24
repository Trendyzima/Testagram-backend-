package main

import (
	"net/mail"
	"strings"
	"testing"
)

func TestBuildSMTPMessageIncludesAlternativeBodiesAndUnsubscribeHeaders(t *testing.T) {
	from, _ := mail.ParseAddress("no-reply@testagram.site")
	to, _ := mail.ParseAddress("person@example.test")
	raw, err := buildSMTPMessage(from, to, queuedMail{
		Subject: "Welcome to Testagram",
		Text: "Hello and welcome",
		HTML: "<p>Hello and welcome</p>",
		Headers: map[string]string{
			"List-Unsubscribe": "<https://testagram.site/api/email/unsubscribe?token=abc>",
			"List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
		},
	}, "testagram-123@testagram.site")
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, expected := range []string{
		"From: no-reply@testagram.site",
		"To: person@example.test",
		"Subject: ",
		"Message-ID: <testagram-123@testagram.site>",
		"X-MJ-CustomID: testagram-123@testagram.site",
		"multipart/alternative",
		"Hello and welcome",
		"<p>Hello and welcome</p>",
		"List-Unsubscribe: <https://testagram.site/api/email/unsubscribe?token=abc>",
		"List-Unsubscribe-Post: List-Unsubscribe=One-Click",
	} {
		if !strings.Contains(got, expected) {
			t.Errorf("SMTP message missing %q: %s", expected, got)
		}
	}
}

func TestBuildSMTPMessageRejectsHeaderInjection(t *testing.T) {
	from, _ := mail.ParseAddress("no-reply@testagram.site")
	to, _ := mail.ParseAddress("person@example.test")
	_, err := buildSMTPMessage(from, to, queuedMail{
		Subject: "Hello",
		HTML: "<p>Hi</p>",
		Headers: map[string]string{"X-Test": "safe\r\nBcc: victim@example.test"},
	}, "testagram-123@testagram.site")
	if err == nil {
		t.Fatal("unsafe custom header was accepted")
	}
}

func TestMailjetSMTPConfigurationRequiresCredentialsAndSTARTTLSPort(t *testing.T) {
	t.Setenv("TESTAGRAM_MAIL_ENCRYPTION_KEY", strings.Repeat("61", 32))
	t.Setenv("AUTH_FROM_EMAIL", "no-reply@testagram.site")
	t.Setenv("MAILJET_SMTP_USERNAME", "api-key")
	t.Setenv("MAILJET_SMTP_PASSWORD", "secret")
	t.Setenv("MAILJET_SMTP_PORT", "25")
	if err := mailSMTPConfigurationError(); err == nil {
		t.Fatal("non-TLS SMTP port was accepted")
	}
	t.Setenv("MAILJET_SMTP_PORT", "587")
	if err := mailSMTPConfigurationError(); err != nil {
		t.Fatalf("valid Mailjet SMTP configuration rejected: %v", err)
	}
}
