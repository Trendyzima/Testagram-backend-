package replication

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
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

var sha256HexPattern = regexp.MustCompile(`^[0-9a-fA-F]{64}package replication

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
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

)

func validateProjection(event Event) (map[string]any, bool, error) {
	if err := event.Validate(); err != nil {
		return nil, false, fmt.Errorf("%w: %v", ErrProjectionRejected, err)
	}
	kind := strings.ToLower(event.EntityType)
	allowed, ok := projectionFields[kind]
	if !ok {
		return nil, false, fmt.Errorf("%w: unsupported replicated entity type %q", ErrProjectionRejected, kind)
	}
	if !strings.HasPrefix(strings.ToLower(event.EventType), kind+".") {
		return nil, false, fmt.Errorf("%w: event type does not match entity type", ErrProjectionRejected)
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
		if kind == "account" {
			if raw, ok := projection["avatar_url"].(string); ok && raw != "" {
				parsed, parseErr := url.Parse(raw)
				if parseErr != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
					return nil, false, fmt.Errorf("%w: avatar_url must be a stable HTTPS URL without query or fragment", ErrProjectionRejected)
				}
			}
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
			objectKey, keyOK := projection["object_key"].(string)
			checksum, checksumOK := projection["checksum_sha256"].(string)
			contentType, typeOK := projection["content_type"].(string)
			if !keyOK || objectKey == "" || strings.Contains(objectKey, "://") || strings.HasPrefix(objectKey, "/") || strings.Contains(objectKey, "..") || strings.ContainsAny(objectKey, "\\?#") || len(objectKey) > 1024 {
				return nil, false, fmt.Errorf("%w: public media object_key must be a relative object key", ErrProjectionRejected)
			}
			if !checksumOK || !sha256HexPattern.MatchString(checksum) {
				return nil, false, fmt.Errorf("%w: checksum_sha256 must be a SHA-256 hex digest", ErrProjectionRejected)
			}
			if !typeOK || (contentType != "image/jpeg" && contentType != "image/png" && contentType != "image/webp" && contentType != "image/gif" && contentType != "image/avif" && contentType != "video/mp4" && contentType != "video/webm") {
				return nil, false, fmt.Errorf("%w: unsupported public media content_type", ErrProjectionRejected)
			}
			size, sizeOK := projection["size_bytes"].(float64)
			if !sizeOK || size < 1 || size > 500*1024*1024 {
				return nil, false, fmt.Errorf("%w: media size_bytes is out of bounds", ErrProjectionRejected)
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
