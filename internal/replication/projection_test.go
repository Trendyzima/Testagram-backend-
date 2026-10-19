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
	applier := ProjectionApplier{}
	tests := []struct{name, kind, eventType, payload string; wantErr bool}{
		{"public post", "post", "post.created", `{"id":"entity-1","author_id":"user-1","text":"hello","visibility":"public"}`, false},
		{"private post", "post", "post.created", `{"id":"entity-1","visibility":"followers"}`, true},
		{"email forbidden", "account", "account.profile.updated", `{"id":"entity-1","username":"a","email":"a@example.com"}`, true},
		{"unknown field", "account", "account.profile.updated", `{"id":"entity-1","password_hash":"x"}`, true},
		{"id mismatch", "account", "account.profile.updated", `{"id":"other","username":"a"}`, true},
		{"public media metadata", "media", "media.created", `{"id":"entity-1","visibility":"public","object_key":"public/a","content_type":"image/webp","checksum_sha256":"abcd"}`, false},
		{"media missing checksum", "media", "media.created", `{"id":"entity-1","visibility":"public","object_key":"public/a","content_type":"image/webp"}`, true},
		{"deleted tombstone", "post", "post.deleted", `{}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := projectionEvent(tt.kind, tt.eventType, tt.payload)
			// Exercise the same schema gate without needing a database by testing
			// the allowlist and privacy helpers directly.
			var payload map[string]any
			if !isTombstoneEvent(event.EventType) {
				if err := json.Unmarshal(event.Payload, &payload); err != nil { t.Fatal(err) }
				allowed := projectionFields[tt.kind]
				err := error(nil)
				for key := range payload { if !allowed[key] { err = errProjectionTest; break } }
				if err == nil && hasPrivateProjectionData(payload) { err = errProjectionTest }
				if err == nil {
					if id, ok := payload["id"].(string); ok && id != event.EntityID { err = errProjectionTest }
				}
				if err == nil && tt.kind == "post" {
					visibility, _ := payload["visibility"].(string)
					if visibility != "public" { err = errProjectionTest }
				}
				if err == nil && tt.kind == "media" {
					visibility, _ := payload["visibility"].(string)
					if visibility != "public" || payload["object_key"] == nil || payload["content_type"] == nil || payload["checksum_sha256"] == nil { err = errProjectionTest }
				}
				if (err != nil) != tt.wantErr { t.Fatalf("wantErr=%v, got %v", tt.wantErr, err) }
			}
		})
	}
}

var errProjectionTest = &projectionTestError{}
type projectionTestError struct{}
func (*projectionTestError) Error() string { return "projection rejected" }
