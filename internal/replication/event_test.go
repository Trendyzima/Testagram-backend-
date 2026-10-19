package replication

import (
	"encoding/json"
	"testing"
	"time"
)

func validEvent() Event {
	return Event{
		ID: "018f1f3a-7b2c-7abc-8def-0123456789ab",
		Origin: "testagram-vps",
		EntityType: "post",
		EntityID: "post:123",
		EntityVersion: 1,
		SchemaVersion: 1,
		EventType: "post.created",
		OccurredAt: time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC),
		Payload: json.RawMessage(`{"visibility":"public","text":"hello"}`),
	}
}

func TestEventValidate(t *testing.T) {
	if err := validEvent().Validate(); err != nil {
		t.Fatalf("valid event rejected: %v", err)
	}
}

func TestEventRejectsSensitivePayloadFields(t *testing.T) {
	for _, payload := range []string{
		`{"access_token":"x"}`,
		`{"media":{"signed_url":"https://example.invalid/?sig=x"}}`,
		`{"Authorization":"Bearer x"}`,
	} {
		event := validEvent()
		event.Payload = json.RawMessage(payload)
		if err := event.Validate(); err == nil {
			t.Errorf("expected sensitive payload rejection for %s", payload)
		}
	}
}

func TestEventRejectsInvalidEnvelope(t *testing.T) {
	cases := map[string]func(*Event){
		"invalid event id": func(e *Event) { e.ID = "not-a-uuid" },
		"zero entity version": func(e *Event) { e.EntityVersion = 0 },
		"zero schema version": func(e *Event) { e.SchemaVersion = 0 },
		"invalid payload": func(e *Event) { e.Payload = json.RawMessage(`{"broken"`) },
		"missing timestamp": func(e *Event) { e.OccurredAt = time.Time{} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			event := validEvent()
			mutate(&event)
			if err := event.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
