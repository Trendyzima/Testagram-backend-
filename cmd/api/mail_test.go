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
		to, _ := body["to"].([]any)
		if len(to) != 1 || to[0] != "person@example.test" || body["from"] != "no-reply@testagram.site" ||
			body["subject"] != "Password reset" || body["html"] != "<p>Reset</p>" {
			t.Errorf("unexpected Cloudflare request payload: %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":{"message_id":"cf-message-123","delivered":["person@example.test"],"queued":[],"permanent_bounces":[],"suppressed_recipients":[]}}`))
	}))
	defer server.Close()

	client := &http.Client{Timeout: time.Second}
	messageID, err := sendCloudflareMailRequest(context.Background(), client, server.URL+"/accounts/0123456789abcdef0123456789abcdef/email/sending/send", "test-token", "no-reply@testagram.site", queuedMail{
		Recipient: "person@example.test", Subject: "Password reset", HTML: "<p>Reset</p>",
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
			name: "provider rejects request", status: http.StatusBadRequest,
			response: `{"success":false,"errors":[{"code":10001,"message":"invalid request"}],"result":null}`,
			wantErr:  "code 10001",
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
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}
