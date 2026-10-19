// Package replication defines the durable, provider-neutral event envelope used
// to synchronize independently operated Testagram and XClone deployments.
package replication

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const MaxPayloadBytes = 1 << 20

var (
	uuidPattern     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-8][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}// Package replication defines the durable, provider-neutral event envelope used
// to synchronize independently operated Testagram and XClone deployments.
package replication

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const MaxPayloadBytes = 1 << 20

var (
)
	identityPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}// Package replication defines the durable, provider-neutral event envelope used
// to synchronize independently operated Testagram and XClone deployments.
package replication

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const MaxPayloadBytes = 1 << 20

var (
)
)

// Event is an immutable, versioned fact. Payload should contain only the minimum
// data needed to replicate the entity; secrets, bearer tokens, and signed URLs
// must never be placed in it.
type Event struct {
	ID            string          `json:"id"`
	Origin        string          `json:"origin"`
	EntityType    string          `json:"entity_type"`
	EntityID      string          `json:"entity_id"`
	EntityVersion int64           `json:"entity_version"`
	SchemaVersion int             `json:"schema_version"`
	EventType     string          `json:"event_type"`
	OccurredAt    time.Time       `json:"occurred_at"`
	Payload       json.RawMessage `json:"payload"`
}

// Validate enforces bounded identifiers and rejects malformed or oversized
// payloads before they enter the durable outbox.
func (e Event) Validate() error {
	if !uuidPattern.MatchString(e.ID) {
		return errors.New("event id must be a UUID")
	}
	for label, value := range map[string]string{
		"origin": e.Origin, "entity_type": e.EntityType,
		"entity_id": e.EntityID, "event_type": e.EventType,
	} {
		if value != strings.TrimSpace(value) || !identityPattern.MatchString(value) {
			return fmt.Errorf("%s is invalid", label)
		}
	}
	if e.EntityVersion < 1 {
		return errors.New("entity_version must be positive")
	}
	if e.SchemaVersion < 1 {
		return errors.New("schema_version must be positive")
	}
	if e.OccurredAt.IsZero() {
		return errors.New("occurred_at is required")
	}
	if len(e.Payload) == 0 || len(e.Payload) > MaxPayloadBytes || !json.Valid(e.Payload) {
		return errors.New("payload must be valid JSON no larger than 1 MiB")
	}
	var payloadObject map[string]any
	if err := json.Unmarshal(e.Payload, &payloadObject); err != nil || payloadObject == nil {
		return errors.New("payload must be a JSON object")
	}
	if bytesContainSensitiveKey(e.Payload) {
		return errors.New("payload must not contain credential or signed-url fields")
	}
	return nil
}

// bytesContainSensitiveKey catches common accidental secret fields. This is a
// guardrail, not a replacement for an allow-listed event payload per entity.
func bytesContainSensitiveKey(raw []byte) bool {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return true
	}
	var walk func(any) bool
	walk = func(v any) bool {
		switch x := v.(type) {
		case map[string]any:
			for k, child := range x {
				key := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(k, "-", "_"), " ", "_"))
				switch key {
				case "authorization", "access_token", "refresh_token", "service_role_key",
					"secret_key", "password", "signed_url", "presigned_url", "upload_url":
					return true
				}
				if walk(child) { return true }
			}
		case []any:
			for _, child := range x { if walk(child) { return true } }
		}
		return false
	}
	return walk(value)
}
