package replication

import (
	"encoding/json"
	"testing"
	"time"
)

func projectionEvent(kind, eventType string, payload string) Event {
	return Event{
		ID: "018f47a2-7b3c-7abc-8def-123456789012", Origin: "xclone",
		EntityType: kind, EntityID: "entity-1", EntityVersion: 1,
		SchemaVersion: 1, EventType: eventType, OccurredAt: time.Now().UTC(),
		Payload: json.RawMessage(payload),
	}
}

func TestProjectionPayloadAllowlist(t *testing.T) {
	tests := []struct {
		name, kind, eventType, payload string
		wantErr bool
	}{
		{"public post", "post", "post.created", `{"id":"entity-1","author_id":"user-1","text":"hello","visibility":"public"}`, false},
		{"private post", "post", "post.created", `{"id":"entity-1","visibility":"followers"}`, true},
		{"email forbidden", "account", "account.profile.updated", `{"id":"entity-1","username":"a","email":"a@example.com"}`, true},
		{"unknown field", "account", "account.profile.updated", `{"id":"entity-1","password_hash":"x"}`, true},
		{"id mismatch", "account", "account.profile.updated", `{"id":"other","username":"a"}`, true},
		{"public media metadata", "media", "media.created", `{"id":"entity-1","visibility":"public","object_key":"public/a","content_type":"image/webp","size_bytes":2048,"checksum_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, false},
		{"media missing checksum", "media", "media.created", `{"id":"entity-1","visibility":"public","object_key":"public/a","content_type":"image/webp","size_bytes":2048}`, true},
		{"deleted tombstone", "post", "post.deleted", `{}`, false},
		{"unsupported entity", "session", "session.created", `{}`, true},
		{"signed avatar URL", "account", "account.profile.updated", `{"id":"entity-1","avatar_url":"https://cdn.example/avatar?token=secret"}`, true},
		{"unsafe object key", "media", "media.created", `{"id":"entity-1","visibility":"public","object_key":"https://bucket/a","content_type":"image/webp","size_bytes":2048,"checksum_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := projectionEvent(tt.kind, tt.eventType, tt.payload)
			_, _, err := validateProjection(event)
			if (err != nil) != tt.wantErr {
				t.Fatalf("wantErr=%v, got %v", tt.wantErr, err)
			}
			if tt.wantErr && err != nil && !isProjectionRejection(err) {
				t.Fatalf("expected permanent projection rejection, got %v", err)
			}
		})
	}
}

func isProjectionRejection(err error) bool {
	return err != nil && (err == ErrProjectionRejected || containsError(err, ErrProjectionRejected))
}

func containsError(err, target error) bool {
	for err != nil {
		if err == target { return true }
		type unwrapper interface { Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok { return false }
		err = u.Unwrap()
	}
	return false
}
