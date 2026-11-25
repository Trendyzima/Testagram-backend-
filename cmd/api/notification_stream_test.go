package main

import (
	"testing"
	"time"
)

func TestNotificationStreamCursorRoundTrip(t *testing.T) {
	want := notificationStreamCursor{
		CreatedAt: time.Date(2026, 11, 25, 10, 30, 0, 0, time.UTC),
		ID:        "123e4567-e89b-12d3-a456-426614174000",
	}
	encoded := encodeNotificationStreamCursor(want)
	got, err := decodeNotificationStreamCursor(encoded)
	if err != nil {
		t.Fatalf("decodeNotificationStreamCursor: %v", err)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) || got.ID != want.ID {
		t.Fatalf("round trip mismatch: got %#v want %#v", got, want)
	}
}

func TestDecodeNotificationStreamCursorRejectsInvalidValues(t *testing.T) {
	for _, value := range []string{
		"not-base64!",
		"eyJjcmVhdGVkX2F0IjoiMjAyNi0xMS0yNVQxMDozMDowMFoiLCJpZCI6Im5vdC11dWlkIn0",
		string(make([]byte, 513)),
	} {
		if _, err := decodeNotificationStreamCursor(value); err == nil {
			t.Fatalf("expected invalid cursor %q to be rejected", value)
		}
	}
}
