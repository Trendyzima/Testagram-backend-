package main

import (
	"testing"
	"time"
)

func TestSocialCursorRoundTrip(t *testing.T) {
	want := socialCursor{CreatedAt: time.Date(2026, 11, 6, 12, 30, 45, 123, time.UTC), ID: "123e4567-e89b-42d3-a456-426614174000"}
	encoded := encodeSocialCursor(want)
	got, err := decodeSocialCursor(encoded)
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) || got.ID != want.ID {
		t.Fatalf("round-trip cursor = %#v, want %#v", got, want)
	}
}

func TestSocialCursorRejectsInvalidValues(t *testing.T) {
	for _, value := range []string{"", "not-base64", "e30", "eyJpZCI6Im5vdC1hLXV1aWQifQ"} {
		if _, err := decodeSocialCursor(value); err == nil {
			t.Errorf("expected cursor %q to be rejected", value)
		}
	}
	if _, err := decodeSocialCursor(string(make([]byte, 513))); err == nil {
		t.Fatal("oversized cursor was accepted")
	}
}

func TestSocialUUIDPattern(t *testing.T) {
	valid := "123e4567-e89b-42d3-a456-426614174000"
	if !socialUUIDPattern.MatchString(valid) {
		t.Fatalf("expected %q to be a valid UUID", valid)
	}
	for _, value := range []string{"", "1", "not-a-uuid", "00000000-0000-0000-0000-000000000000"} {
		if socialUUIDPattern.MatchString(value) {
			t.Errorf("expected %q to be rejected", value)
		}
	}
}

func TestSocialInteractionAvatarURLUsesPublicMediaBase(t *testing.T) {
	t.Setenv("MEDIA_PUBLIC_BASE_URL", "https://cdn.testagram.site/media")
	got := publicMediaURL("avatars/account/profile.png")
	want := "https://cdn.testagram.site/media/avatars/account/profile.png"
	if got != want {
		t.Fatalf("publicMediaURL() = %q, want %q", got, want)
	}
}
