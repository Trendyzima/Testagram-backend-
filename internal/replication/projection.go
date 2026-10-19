package replication

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ProjectionApplier stores a deliberately limited public-data projection. It
// is not a replacement for the destination's authoritative account or post
// tables; private fields must never be replicated through this generic path.
type ProjectionApplier struct{}

var projectionFields = map[string]map[string]bool{
	"account": {
		"id": true, "username": true, "display_name": true, "bio": true,
		"avatar_key": true, "avatar_url": true, "website": true,
	},
	"post": {
		"id": true, "author_id": true, "text": true, "content": true,
		"visibility": true, "created_at": true, "updated_at": true,
		"media_ids": true, "language": true,
	},
	"media": {
		"id": true, "owner_id": true, "object_key": true, "content_type": true,
		"size_bytes": true, "checksum_sha256": true, "visibility": true,
		"created_at": true,
	},
}

var ErrProjectionRejected = errors.New("replication projection rejected")

func validateProjection(event Event) (map[string]any, bool, error) {
	if err := event.Validate(); err != nil {
		return nil, false, fmt.Errorf("%w: %v", ErrProjectionRejected, err)
	}
	kind := strings.ToLower(event.EntityType)
	allowed, ok := projectionFields[kind]
	if !ok {
		return nil, false, fmt.Errorf("%w: unsupported replicated entity type %q", ErrProjectionRejected, kind)
	}
	deleted := isTombstoneEvent(event.EventType)
	var projection map[string]any
	if deleted {
		projection = map[string]any{}
	} else {
		if err := json.Unmarshal(event.Payload, &projection); err != nil || projection == nil {
			return nil, false, fmt.Errorf("%w: projection payload must be a JSON object", ErrProjectionRejected)
		}
		for key := range projection {
			if !allowed[key] {
				return nil, false, fmt.Errorf("%w: field %q is not allowed in %s projection", ErrProjectionRejected, key, kind)
			}
		}
		if hasPrivateProjectionData(projection) {
			return nil, false, fmt.Errorf("%w: projection contains private or credential-like data", ErrProjectionRejected)
		}
		if id, ok := projection["id"].(string); ok && id != event.EntityID {
			return nil, false, fmt.Errorf("%w: projection id does not match event entity_id", ErrProjectionRejected)
		}
		if kind == "post" {
			visibility, _ := projection["visibility"].(string)
			if visibility != "public" {
				return nil, false, fmt.Errorf("%w: only explicitly public posts may be projected", ErrProjectionRejected)
			}
		}
		if kind == "media" {
			visibility, _ := projection["visibility"].(string)
			if visibility != "public" {
				return nil, false, fmt.Errorf("%w: only explicitly public media metadata may be projected", ErrProjectionRejected)
			}
			for _, key := range []string{"object_key", "checksum_sha256", "content_type"} {
				if _, exists := projection[key]; !exists {
					return nil, false, fmt.Errorf("%w: public media projection requires %s", ErrProjectionRejected, key)
				}
			}
		}
	}
	return projection, deleted, nil
}

func (ProjectionApplier) Apply(ctx context.Context, tx pgx.Tx, event Event) error {
	if tx == nil {
		return errors.New("projection transaction is required")
	}
	projection, deleted, err := validateProjection(event)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(projection)
	if err != nil {
		return fmt.Errorf("encode public projection: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO platform_replication_projection
		  (origin, entity_type, entity_id, entity_version, schema_version, payload, tombstoned)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7)
		ON CONFLICT (origin, entity_type, entity_id) DO UPDATE
		SET entity_version=EXCLUDED.entity_version,
		    schema_version=EXCLUDED.schema_version,
		    payload=EXCLUDED.payload,
		    tombstoned=EXCLUDED.tombstoned,
		    updated_at=now()
		WHERE platform_replication_projection.entity_version < EXCLUDED.entity_version`,
		event.Origin, strings.ToLower(event.EntityType), event.EntityID, event.EntityVersion, event.SchemaVersion, payload, deleted)
	if err != nil {
		return fmt.Errorf("upsert public replication projection: %w", err)
	}
	return nil
}

func hasPrivateProjectionData(value any) bool {
	forbidden := map[string]bool{
		"authorization": true, "access_token": true, "refresh_token": true,
		"password": true, "email": true, "phone": true, "private_key": true,
		"service_role_key": true, "signed_url": true, "presigned_url": true,
		"upload_url": true, "session": true,
	}
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), " ", "_"))
			if forbidden[normalized] || hasPrivateProjectionData(child) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if hasPrivateProjectionData(child) {
				return true
			}
		}
	}
	return false
}
