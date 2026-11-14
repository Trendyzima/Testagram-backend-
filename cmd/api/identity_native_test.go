package main

import "testing"

func TestIdentityEvidenceExtensionAllowlist(t *testing.T) {
	cases := []struct {
		contentType string
		want string
		ok bool
	}{
		{"image/jpeg", ".jpg", true},
		{"image/png", ".png", true},
		{"image/webp", ".webp", true},
		{"application/pdf", ".pdf", true},
		{"image/svg+xml", "", false},
		{"text/html", "", false},
		{"application/zip", "", false},
	}
	for _, tc := range cases {
		got, ok := identityEvidenceExtension(tc.contentType)
		if got != tc.want || ok != tc.ok {
			t.Errorf("identityEvidenceExtension(%q) = (%q,%v), want (%q,%v)", tc.contentType, got, ok, tc.want, tc.ok)
		}
	}
}

func TestIdentityRequestTokenShapes(t *testing.T) {
	if !identityApplicationIDPattern.MatchString("123e4567-e89b-42d3-a456-426614174000") {
		t.Fatal("valid UUID-shaped identifier rejected")
	}
	if identityApplicationIDPattern.MatchString("not-a-uuid") {
		t.Fatal("invalid identifier accepted")
	}
	if !identityIdempotencyKeyPattern.MatchString("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ") {
		t.Fatal("valid URL-safe idempotency key rejected")
	}
	if identityIdempotencyKeyPattern.MatchString("short") {
		t.Fatal("short idempotency key accepted")
	}
}

func TestIdentityEvidenceContentTypeSniffing(t *testing.T) {
	cases := []struct {
		name string
		contentType string
		payload []byte
		want bool
	}{
		{"jpeg", "image/jpeg", []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00"), true},
		{"png", "image/png", []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), true},
		{"webp", "image/webp", []byte("RIFF\x10\x00\x00\x00WEBPVP8 "), true},
		{"pdf", "application/pdf", []byte("%PDF-1.7\n"), true},
		{"html masquerading as jpeg", "image/jpeg", []byte("<html><body>not an image</body></html>"), false},
		{"jpeg masquerading as pdf", "application/pdf", []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00"), false},
		{"empty", "image/png", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := identityEvidenceMatchesContentType(tc.payload, tc.contentType); got != tc.want {
				t.Fatalf("identityEvidenceMatchesContentType() = %v, want %v", got, tc.want)
			}
		})
	}
}
