package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMailPlainTextAlternativeStripsMarkupAndDecodesEntities(t *testing.T) {
	got := mailPlainTextFromHTML("<p>Hello &amp; welcome</p><p>Open <strong>Testagram</strong></p>")
	if got != "Hello & welcome\nOpen Testagram" {
		t.Fatalf("plain-text alternative = %q", got)
	}
}

func TestMailPlainTextPreservesUnsubscribeLinkDestination(t *testing.T) {
	got := mailPlainTextFromHTML(`<p>Manage your settings: <a href="https://testagram.site/api/email/unsubscribe?token=abc">unsubscribe</a></p>`)
	if !strings.Contains(got, "unsubscribe (https://testagram.site/api/email/unsubscribe?token=abc)") {
		t.Fatalf("plain-text alternative lost unsubscribe destination: %q", got)
	}
}

func TestBrandedMailShellEscapesSubjectAndIncludesAccessibleLayout(t *testing.T) {
	t.Setenv("AUTH_PUBLIC_BASE_URL", "https://testagram.site")
	got := brandedTestagramMailHTML("<welcome>", "<p>Hello</p>")
	if !strings.Contains(got, "&lt;welcome&gt;") || !strings.Contains(got, "role=\"presentation\"") ||
		!strings.Contains(got, "lang=\"en\"") || !strings.Contains(got, "<p>Hello</p>") {
		t.Fatalf("branded email shell is missing expected accessible structure: %s", got)
	}
}

func TestMailCipherEncryptsAndDecrypts(t *testing.T) {
	key := strings.Repeat("ab", 32)
	t.Setenv("TESTAGRAM_MAIL_ENCRYPTION_KEY", key)

	aead, err := mailCipher()
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte(`{"recipient":"person@example.test","subject":"test","html":"secret-link"}`)
	nonce := make([]byte, aead.NonceSize())
	for i := range nonce {
		nonce[i] = byte(i + 1)
	}
	ciphertext := aead.Seal(nil, nonce, plain, []byte("testagram-mail-v1"))
	if string(ciphertext) == string(plain) {
		t.Fatal("mail payload was not encrypted")
	}
	got, err := aead.Open(nil, nonce, ciphertext, []byte("testagram-mail-v1"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(plain) {
		t.Fatalf("decrypted payload mismatch: %q", got)
	}
	if _, err := hex.DecodeString(key); err != nil {
		t.Fatal(err)
	}
}

func TestMailCipherRejectsMissingOrWeakKey(t *testing.T) {
	keys := []string{"", "abcd", strings.Repeat("z", 64), strings.Repeat("ab", 31)}
	for i, key := range keys {
		t.Run(fmt.Sprintf("case-%d", i), func(t *testing.T) {
			t.Setenv("TESTAGRAM_MAIL_ENCRYPTION_KEY", key)
			if _, err := mailCipher(); err == nil {
				t.Fatal("invalid mail encryption key accepted")
			}
		})
	}
}

func TestCloudflareMailRequiresConfiguredAccount(t *testing.T) {
	t.Setenv("CLOUDFLARE_EMAIL_ACCOUNT_ID", "")
	t.Setenv("CLOUDFLARE_EMAIL_API_TOKEN", "")
	t.Setenv("AUTH_FROM_EMAIL", "")

	svc := &service{}
	_, err := svc.sendCloudflareMail(context.Background(), queuedMail{
		Recipient: "person@example.test",
		Subject:   "test",
		HTML:      "<p>test</p>",
	})
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("expected safe not-configured error, got %v", err)
	}
}

func TestCloudflareMailRequestContract(t *testing.T) {
	var requestSeen bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestSeen = true
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/accounts/0123456789abcdef0123456789abcdef/email/sending/send" {
			t.Errorf("path = %s, unexpected Cloudflare endpoint", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("authorization header = %q, want bearer token", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("content type = %q, want application/json", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if body["to"] != "person@example.test" || body["from"] != "no-reply@testagram.site" ||
			body["subject"] != "Password reset" || body["html"] != "<p>Reset</p>" || body["text"] != "Reset text" {
			t.Errorf("unexpected Cloudflare request payload: %#v", body)
		}
		if headers, ok := body["headers"].(map[string]any); !ok || headers["List-Unsubscribe-Post"] != "List-Unsubscribe=One-Click" {
			t.Errorf("Cloudflare request did not include RFC 8058 unsubscribe headers: %#v", body["headers"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":{"message_id":"cf-message-123","delivered":["person@example.test"],"queued":[],"permanent_bounces":[],"suppressed_recipients":[]}}`))
	}))
	defer server.Close()

	client := &http.Client{Timeout: time.Second}
	messageID, err := sendCloudflareMailRequest(context.Background(), client, server.URL+"/accounts/0123456789abcdef0123456789abcdef/email/sending/send", "test-token", "no-reply@testagram.site", queuedMail{
		Recipient: "person@example.test", Subject: "Password reset", HTML: "<p>Reset</p>", Text: "Reset text",
		Headers: map[string]string{"List-Unsubscribe": "<https://testagram.site/api/email/unsubscribe?token=abc>", "List-Unsubscribe-Post": "List-Unsubscribe=One-Click"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !requestSeen {
		t.Fatal("Cloudflare endpoint was not called")
	}
	if messageID != "cf-message-123" {
		t.Fatalf("message ID = %q, want cf-message-123", messageID)
	}
}

func TestCloudflareMailRequestRejectsProviderErrorsAndSuppression(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		response  string
		wantErr   string
		permanent bool
	}{
		{
			name: "provider rejects request permanently", status: http.StatusBadRequest,
			response:  `{"success":false,"errors":[{"code":10001,"message":"invalid request"}],"result":null}`,
			permanent: true,
		},
		{
			name: "rate limited request is safely retryable", status: http.StatusTooManyRequests,
			response: `{"success":false,"errors":[{"code":10002,"message":"rate limited"}],"result":null}`,
			wantErr:  "temporarily rejected",
		},
		{
			name: "suppressed recipient", status: http.StatusOK,
			response:  `{"success":true,"errors":[],"result":{"message_id":"cf-suppressed","delivered":[],"queued":[],"permanent_bounces":[],"suppressed_recipients":["person@example.test"]}}`,
			permanent: true,
		},
		{
			name: "provider server error has ambiguous outcome", status: http.StatusBadGateway,
			response: `{"success":false,"errors":[],"result":null}`,
			wantErr:  "delivery outcome is unknown",
		},
		{
			name: "empty accepted recipient set", status: http.StatusOK,
			response: `{"success":true,"errors":[],"result":{"message_id":"cf-empty","delivered":[],"queued":[],"permanent_bounces":[],"suppressed_recipients":[]}}`,
			wantErr:  "delivery outcome is unknown",
		},
		{
			name: "missing provider message ID", status: http.StatusOK,
			response: `{"success":true,"errors":[],"result":{"delivered":["person@example.test"],"queued":[],"permanent_bounces":[],"suppressed_recipients":[]}}`,
			wantErr:  "without a provider message ID",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()
			_, err := sendCloudflareMailRequest(context.Background(), server.Client(), server.URL, "test-token", "no-reply@testagram.site", queuedMail{
				Recipient: "person@example.test", Subject: "Test", HTML: "<p>Test</p>",
			})
			if tt.name == "missing provider message ID" || tt.name == "provider server error has ambiguous outcome" || tt.name == "empty accepted recipient set" {
				var ambiguous ambiguousMailError
				if !errors.As(err, &ambiguous) {
					t.Fatalf("error = %v, want ambiguousMailError", err)
				}
				return
			}
			if tt.permanent {
				var permanent permanentMailError
				if !errors.As(err, &permanent) {
					t.Fatalf("error = %v, want permanentMailError", err)
				}
				if tt.name == "provider rejects request permanently" {
					if permanent.apiCode != 10001 || permanent.httpStatus != http.StatusBadRequest ||
						permanent.code != "CLOUDFLARE_API_10001" {
						t.Fatalf("provider rejection diagnostics = %#v, want HTTP 400 / API 10001", permanent)
					}
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}
