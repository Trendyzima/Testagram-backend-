package main

import (
	"strings"
	"testing"
)

func TestValidateCreatePostV1(t *testing.T) {
	tests := []struct {
		name       string
		input      createPostV1Request
		key        string
		wantCode   string
		wantText   string
		wantVis    string
	}{
		{name: "valid defaults visibility", input: createPostV1Request{Content: "  hello world  "}, key: "request-key-0001", wantText: "hello world", wantVis: "public"},
		{name: "valid followers visibility", input: createPostV1Request{Content: "hello", Visibility: "FOLLOWERS"}, key: "request-key-0002", wantText: "hello", wantVis: "followers"},
		{name: "empty body rejected", input: createPostV1Request{Content: "  "}, key: "request-key-0003", wantCode: "INVALID_POST_CONTENT"},
		{name: "body over limit rejected", input: createPostV1Request{Content: strings.Repeat("x", 5001)}, key: "request-key-0004", wantCode: "INVALID_POST_CONTENT"},
		{name: "invalid visibility rejected", input: createPostV1Request{Content: "hello", Visibility: "everyone"}, key: "request-key-0005", wantCode: "INVALID_POST_VISIBILITY"},
		{name: "missing idempotency key rejected", input: createPostV1Request{Content: "hello"}, key: "", wantCode: "IDEMPOTENCY_KEY_REQUIRED"},
		{name: "short idempotency key rejected", input: createPostV1Request{Content: "hello"}, key: "short", wantCode: "IDEMPOTENCY_KEY_REQUIRED"},
		{name: "long idempotency key rejected", input: createPostV1Request{Content: "hello"}, key: strings.Repeat("k", 129), wantCode: "IDEMPOTENCY_KEY_REQUIRED"},
		{name: "CRLF idempotency key rejected", input: createPostV1Request{Content: "hello"}, key: "valid-key\r\nInjected: yes", wantCode: "IDEMPOTENCY_KEY_REQUIRED"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := test.input
			code, _ := validateCreatePostV1(&input, test.key)
			if code != test.wantCode {
				t.Fatalf("validation code = %q, want %q", code, test.wantCode)
			}
			if code == "" {
				if input.Content != test.wantText {
					t.Errorf("normalized content = %q, want %q", input.Content, test.wantText)
				}
				if input.Visibility != test.wantVis {
					t.Errorf("normalized visibility = %q, want %q", input.Visibility, test.wantVis)
				}
			}
		})
	}
}
