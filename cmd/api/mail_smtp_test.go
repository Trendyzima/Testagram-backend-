package main

import (
	"context"
	"errors"
	"net"
	"net/mail"
	"strings"
	"testing"
)

func TestNativeMailConfigurationNeedsNoProviderCredentials(t *testing.T) {
	t.Setenv("TESTAGRAM_MAIL_ENCRYPTION_KEY", strings.Repeat("61", 32))
	t.Setenv("MAILJET_API_KEY", "")
	t.Setenv("MAILJET_SECRET_KEY", "")
	t.Setenv("AUTH_FROM_EMAIL", "")
	if err := mailProviderConfigurationError(); err != nil {
		t.Fatalf("native mail configuration unexpectedly requires a provider: %v", err)
	}
}

func TestNativeMailRuntimeRequiresVERPAndDKIMConfiguration(t *testing.T) {
	t.Setenv("TESTAGRAM_MAIL_ENCRYPTION_KEY", strings.Repeat("61", 32))
	t.Setenv("TESTAGRAM_MAIL_VERP_KEY", "")
	t.Setenv("TESTAGRAM_DKIM_PRIVATE_KEY_FILE", "")
	if err := mailRuntimeConfigurationError(); err == nil {
		t.Fatal("runtime accepted missing dedicated VERP and DKIM keys")
	}

	t.Setenv("TESTAGRAM_MAIL_VERP_KEY", strings.Repeat("62", 32))
	if err := mailRuntimeConfigurationError(); err == nil {
		t.Fatal("runtime accepted missing DKIM private key")
	}

	_, keyPath := writeTestDKIMKey(t, 0600)
	t.Setenv("TESTAGRAM_DKIM_PRIVATE_KEY_FILE", keyPath)
	if err := mailRuntimeConfigurationError(); err != nil {
		t.Fatalf("runtime rejected valid native mail keys: %v", err)
	}
}

func TestNativeMailMXHostsSortsRecipientMXByPreference(t *testing.T) {
	hosts, err := nativeMailMXHosts(context.Background(), "example.test", func(context.Context, string) ([]*net.MX, error) {
		return []*net.MX{{Host: "secondary.example.test.", Pref: 20}, {Host: "primary.example.test.", Pref: 10}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 2 || hosts[0].Host != "primary.example.test." || hosts[1].Host != "secondary.example.test." {
		t.Fatalf("MX hosts were not preference-sorted: %#v", hosts)
	}
}

func TestNativeMailMXHostsUsesAddressFallbackOnlyWhenMXIsAbsent(t *testing.T) {
	for _, lookup := range []func(context.Context, string) ([]*net.MX, error){
		func(context.Context, string) ([]*net.MX, error) { return nil, nil },
		func(context.Context, string) ([]*net.MX, error) {
			return nil, &net.DNSError{Err: "no such host", Name: "example.test", IsNotFound: true}
		},
	} {
		hosts, err := nativeMailMXHosts(context.Background(), "example.test", lookup)
		if err != nil {
			t.Fatal(err)
		}
		if len(hosts) != 1 || hosts[0].Host != "example.test" {
			t.Fatalf("missing-MX fallback is incorrect: %#v", hosts)
		}
	}
}

func TestNativeMailMXHostsDoesNotHideTransientDNSFailures(t *testing.T) {
	_, err := nativeMailMXHosts(context.Background(), "example.test", func(context.Context, string) ([]*net.MX, error) {
		return nil, errors.New("temporary resolver timeout")
	})
	if err == nil || !strings.Contains(err.Error(), "MX lookup failed") {
		t.Fatalf("transient DNS failure was hidden: %v", err)
	}
}

func TestBuildNativeSMTPMessagePreservesUnsubscribeHeaders(t *testing.T) {
	to, err := mail.ParseAddress("person@example.test")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := buildNativeSMTPMessage(to, queuedMail{
		Recipient: "person@example.test",
		Subject:   "Testagram notice",
		Text:      "Hello",
		HTML:      "<p>Hello</p>",
		Headers: map[string]string{
			"List-Unsubscribe":      "<https://testagram.site/api/email/unsubscribe?token=abc>",
			"List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
		},
	}, "abc123@testagram.site")
	if err != nil {
		t.Fatal(err)
	}
	got := string(payload)
	for _, want := range []string{
		"From: Testagram <no-reply@testagram.site>",
		"Message-ID: <abc123@testagram.site>",
		"List-Unsubscribe-Post: List-Unsubscribe=One-Click",
		"multipart/alternative",
		"text/plain",
		"text/html",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("native message missing %q: %s", want, got)
		}
	}
}

func TestBuildNativeSMTPMessageRejectsUnsafeHeaders(t *testing.T) {
	to, _ := mail.ParseAddress("person@example.test")
	_, err := buildNativeSMTPMessage(to, queuedMail{
		Subject: "Test",
		HTML:    "<p>Hi</p>",
		Headers: map[string]string{"X-Test": "bad"},
	}, "abc@testagram.site")
	if err == nil {
		t.Fatal("unsupported custom header was accepted")
	}
}
