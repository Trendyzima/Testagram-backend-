package main

import (
	"testing"
	"time"
)

func TestMixedHomeFeedCursorRoundTripsIndependentSourcePositions(t *testing.T) {
	createdAt := time.Date(2026, 11, 16, 16, 0, 0, 0, time.UTC)
	cursor := homeFeedCursor{
		RemoteOffset: 7,
		Sources: []homeFeedSourcePosition{
			{Source: homeFeedSourceNative, CreatedAt: createdAt, ID: "123e4567-e89b-42d3-a456-426614174000"},
			{Source: homeFeedSourceActivityPub, Cursor: "activitypub-page-2"},
			{Source: homeFeedSourceBluesky, Identity: "did:plc:222222222222222222222222", Cursor: "atproto-author-page-2"},
		},
	}
	decoded, err := decodeHomeFeedCursor(encodeHomeFeedCursor(cursor))
	if err != nil {
		t.Fatalf("mixed source cursor did not decode: %v", err)
	}
	if decoded.Version != 2 || decoded.RemoteOffset != 7 || len(decoded.Sources) != 3 {
		t.Fatalf("mixed source cursor lost its version, rotation offset or positions: %#v", decoded)
	}
	native, ok := homeFeedSourcePositionFor(decoded, homeFeedSourceNative, "")
	if !ok || native.ID != cursor.Sources[0].ID || !native.CreatedAt.Equal(createdAt) {
		t.Fatalf("native UUID position was not preserved: %#v", native)
	}
	ap, ok := homeFeedSourcePositionFor(decoded, homeFeedSourceActivityPub, "")
	if !ok || ap.Cursor != "activitypub-page-2" {
		t.Fatalf("ActivityPub cursor was not preserved: %#v", ap)
	}
	at, ok := homeFeedSourcePositionFor(decoded, homeFeedSourceBluesky, "did:plc:222222222222222222222222")
	if !ok || at.Cursor != "atproto-author-page-2" {
		t.Fatalf("per-DID AT Protocol cursor was not preserved: %#v", at)
	}
}

func TestMixedHomeFeedCursorRejectsInvalidRemoteCursorState(t *testing.T) {
	base := homeFeedCursor{
		Sources: []homeFeedSourcePosition{
			{Source: homeFeedSourceNative, CreatedAt: time.Date(2026, 11, 16, 16, 0, 0, 0, time.UTC), ID: "123e4567-e89b-42d3-a456-426614174000"},
			{Source: homeFeedSourceBluesky, Identity: "did:plc:222222222222222222222222", Cursor: "valid"},
		},
	}
	cases := map[string]homeFeedCursor{
		"oversized per-author cursor": {
			Sources: []homeFeedSourcePosition{
				base.Sources[0],
				{Source: homeFeedSourceBluesky, Identity: "did:plc:222222222222222222222222", Cursor: string(make([]byte, 513))},
			},
		},
		"exhausted source with live cursor": {
			Sources: []homeFeedSourcePosition{
				base.Sources[0],
				{Source: homeFeedSourceActivityPub, Cursor: "still-live", Exhausted: true},
			},
		},
		"invalid rotation offset": {
			Sources: base.Sources, RemoteOffset: 501,
		},
		"unknown source": {
			Sources: []homeFeedSourcePosition{
				base.Sources[0],
				{Source: "unknown", Cursor: "cursor"},
			},
		},
	}
	for name, cursor := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeHomeFeedCursor(encodeHomeFeedCursor(cursor)); err == nil {
				t.Fatal("invalid mixed source cursor was accepted")
			}
		})
	}
}

func TestHomeFeedSourcePositionUpsertDoesNotDuplicateSourceIdentity(t *testing.T) {
	positions := []homeFeedSourcePosition{
		{Source: homeFeedSourceBluesky, Identity: "did:plc:222222222222222222222222", Cursor: "page-1"},
	}
	positions = upsertHomeFeedSourcePosition(positions, homeFeedSourcePosition{
		Source: homeFeedSourceBluesky, Identity: "did:plc:222222222222222222222222", Cursor: "page-2",
	})
	if len(positions) != 1 || positions[0].Cursor != "page-2" {
		t.Fatalf("source cursor update duplicated or failed to replace the DID position: %#v", positions)
	}
}
