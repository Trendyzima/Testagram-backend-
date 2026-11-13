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
