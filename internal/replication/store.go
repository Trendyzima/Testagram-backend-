package replication

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrLeaseNotHeld = errors.New("replication outbox lease is not held")
	ErrDuplicateVersion = errors.New("replication entity version already exists")
)

// Store is the durable PostgreSQL boundary for replication. Redis/Valkey is
// intentionally not involved in the source-of-truth delivery queue.
type Store struct {
	db *pgxpool.Pool
}

func NewStore(db *pgxpool.Pool) (*Store, error) {
	if db == nil {
		return nil, errors.New("replication database pool is required")
	}
	return &Store{db: db}, nil
}

// Publish inserts an immutable event. Call this in the same database
// transaction as the business mutation to guarantee transactional-outbox
// semantics; this convenience method is for standalone events only.
func (s *Store) Publish(ctx context.Context, event Event) error {
	if err := event.Validate(); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO platform_replication_outbox
		  (event_id, origin, entity_type, entity_id, entity_version,
		   schema_version, event_type, occurred_at, payload)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb)
		ON CONFLICT (event_id) DO NOTHING`,
		event.ID, event.Origin, event.EntityType, event.EntityID,
		event.EntityVersion, event.SchemaVersion, event.EventType,
		event.OccurredAt.UTC(), []byte(event.Payload))
	if err != nil {
		return fmt.Errorf("insert replication event: %w", err)
	}
	return nil
}

// PublishTx is the transactional form: call it with the same transaction that
// writes the entity being changed. The entity write and outbox event then commit
// or roll back together.
func PublishTx(ctx context.Context, tx pgx.Tx, event Event) error {
	if tx == nil {
		return errors.New("replication transaction is required")
	}
	if err := event.Validate(); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO platform_replication_outbox
		  (event_id, origin, entity_type, entity_id, entity_version,
		   schema_version, event_type, occurred_at, payload)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb)
		ON CONFLICT (event_id) DO NOTHING`,
		event.ID, event.Origin, event.EntityType, event.EntityID,
		event.EntityVersion, event.SchemaVersion, event.EventType,
		event.OccurredAt.UTC(), []byte(event.Payload))
	if err != nil {
		return fmt.Errorf("insert replication event in transaction: %w", err)
	}
	return nil
}

// ClaimBatch leases ready events using SKIP LOCKED, allowing multiple relay
// instances to run concurrently without claiming the same row at once. The
// caller must MarkDelivered or Retry before the lease expires.
func (s *Store) ClaimBatch(ctx context.Context, limit int, lease time.Duration) ([]Event, error) {
	if limit < 1 || limit > 500 {
		return nil, errors.New("replication batch size must be between 1 and 500")
	}
	if lease < 5*time.Second || lease > 10*time.Minute {
		return nil, errors.New("replication lease must be between 5 seconds and 10 minutes")
	}
	rows, err := s.db.Query(ctx, `
		WITH picked AS (
			SELECT event_id
			FROM platform_replication_outbox
			WHERE delivered_at IS NULL
			  AND available_at <= now()
			  AND (lease_until IS NULL OR lease_until < now())
			ORDER BY created_at, event_id
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		UPDATE platform_replication_outbox AS o
		SET lease_until = now() + ($2 * interval '1 second'),
		    attempts = attempts + 1
		FROM picked
		WHERE o.event_id = picked.event_id
		RETURNING o.event_id::text, o.origin, o.entity_type, o.entity_id,
		          o.entity_version, o.schema_version, o.event_type,
		          o.occurred_at, o.payload::text`, limit, int64(lease/time.Second))
	if err != nil {
		return nil, fmt.Errorf("claim replication events: %w", err)
	}
	defer rows.Close()
	events := make([]Event, 0, limit)
	for rows.Next() {
		var e Event
		var payload string
		if err := rows.Scan(&e.ID, &e.Origin, &e.EntityType, &e.EntityID,
			&e.EntityVersion, &e.SchemaVersion, &e.EventType, &e.OccurredAt, &payload); err != nil {
			return nil, fmt.Errorf("scan replication event: %w", err)
		}
		e.Payload = []byte(payload)
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate replication events: %w", err)
	}
	return events, nil
}

func (s *Store) MarkDelivered(ctx context.Context, eventID string) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE platform_replication_outbox
		SET delivered_at = now(), lease_until = NULL, last_error_code = NULL
		WHERE event_id = $1::uuid AND delivered_at IS NULL AND lease_until > now()`, eventID)
	if err != nil {
		return fmt.Errorf("mark replication event delivered: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrLeaseNotHeld
	}
	return nil
}

// Retry releases a lease and applies bounded exponential backoff. Store only a
// stable error code, never raw response bodies, credentials, or private payloads.
func (s *Store) Retry(ctx context.Context, eventID, errorCode string) error {
	if len(errorCode) > 64 || errorCode == "" {
		return errors.New("replication error code must be 1-64 characters")
	}
	for _, r := range errorCode {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') &&
			!(r >= '0' && r <= '9') && r != '_' && r != '-' {
			return errors.New("replication error code contains invalid characters")
		}
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE platform_replication_outbox
		SET lease_until = NULL,
		    available_at = now() + LEAST(3600, (5 * power(2, LEAST(attempts, 10)))::integer) * interval '1 second',
		    last_error_code = $2
		WHERE event_id = $1::uuid AND delivered_at IS NULL AND lease_until IS NOT NULL`,
		eventID, errorCode)
	if err != nil {
		return fmt.Errorf("schedule replication retry: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrLeaseNotHeld
	}
	return nil
}

// RecordInbox is an idempotent receipt operation. Applying an event to domain
// tables must happen in the same transaction as this receipt in the consumer;
// this method intentionally does not pretend that receipt alone means applied.
func (s *Store) RecordInbox(ctx context.Context, event Event) (bool, error) {
	if err := event.Validate(); err != nil {
		return false, err
	}
	envelope, err := json.Marshal(event)
	if err != nil {
		return false, fmt.Errorf("marshal replication event: %w", err)
	}
	hash := sha256.Sum256(envelope)
	tag, err := s.db.Exec(ctx, `
		INSERT INTO platform_replication_inbox
		  (event_id, origin, entity_type, entity_id, entity_version,
		   schema_version, event_type, payload_hash, payload, outcome)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, 'received')
		ON CONFLICT (event_id) DO NOTHING`,
		event.ID, event.Origin, event.EntityType, event.EntityID,
		event.EntityVersion, event.SchemaVersion, event.EventType, hash[:], envelope)
	if err != nil {
		return false, fmt.Errorf("record replication inbox event: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return true, nil
	}
	var existingHash []byte
	if err := s.db.QueryRow(ctx, `SELECT payload_hash FROM platform_replication_inbox WHERE event_id = $1::uuid`, event.ID).Scan(&existingHash); err != nil {
		return false, fmt.Errorf("read existing replication receipt: %w", err)
	}
	if !hmac.Equal(existingHash, hash[:]) {
		return false, errors.New("replication event id reused with different payload")
	}
	return false, nil
}

// WithTx lets domain handlers write the business row and outbox event atomically.
// The callback owns all SQL and must not commit the supplied transaction.
func (s *Store) WithTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin replication transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit replication transaction: %w", err)
	}
	return nil
}

 
// ApplyInboxTx records receipt, applies the domain projection, and marks the
// event applied in one transaction. A duplicate event ID with a different
// payload is rejected instead of being silently treated as a successful replay.
func (s *Store) ApplyInboxTx(ctx context.Context, event Event, apply ApplyFunc) (bool, error) {
	if err := event.Validate(); err != nil {
		return false, err
	}
	if apply == nil {
		return false, errors.New("event apply function is required")
	}
	envelope, err := json.Marshal(event)
	if err != nil {
		return false, fmt.Errorf("marshal replication event: %w", err)
	}
	hash := sha256.Sum256(envelope)
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("begin replication apply transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var insertedID string
	err = tx.QueryRow(ctx, `
		INSERT INTO platform_replication_inbox
		  (event_id, origin, entity_type, entity_id, entity_version,
		   schema_version, event_type, payload_hash, payload, outcome)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, 'received')
		ON CONFLICT (event_id) DO NOTHING
		RETURNING event_id::text`,
		event.ID, event.Origin, event.EntityType, event.EntityID,
		event.EntityVersion, event.SchemaVersion, event.EventType, hash[:], envelope).Scan(&insertedID)
	if errors.Is(err, pgx.ErrNoRows) {
		var existingHash []byte
		if readErr := tx.QueryRow(ctx, `SELECT payload_hash FROM platform_replication_inbox WHERE event_id = $1::uuid`, event.ID).Scan(&existingHash); readErr != nil {
			return false, fmt.Errorf("read duplicate replication receipt: %w", readErr)
		}
		if !hmac.Equal(existingHash, hash[:]) {
			return false, errors.New("replication event id reused with different payload")
		}
		if err := tx.Commit(ctx); err != nil {
			return false, fmt.Errorf("commit duplicate receipt check: %w", err)
		}
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("insert replication inbox receipt: %w", err)
	}
	if err := apply(ctx, tx, event); err != nil {
		return false, fmt.Errorf("apply replication event: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE platform_replication_inbox
		SET applied_at = now(), outcome = 'applied', error_code = NULL
		WHERE event_id = $1::uuid AND outcome = 'received'`, event.ID)
	if err != nil {
		return false, fmt.Errorf("mark replication event applied: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return false, errors.New("replication inbox state changed unexpectedly")
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit replication projection: %w", err)
	}
	return true, nil
}

 
// ApplyPendingBatch applies queued inbox events one transaction per event. The
// domain callback must enforce entity-version ordering and tombstone semantics.
// Failed projections remain retryable and do not block unrelated events.
func (s *Store) ApplyPendingBatch(ctx context.Context, limit int, apply ApplyFunc) (int, error) {
	if limit < 1 || limit > 100 {
		return 0, errors.New("inbox batch size must be between 1 and 100")
	}
	if apply == nil {
		return 0, errors.New("event apply function is required")
	}
	rows, err := s.db.Query(ctx, `
		SELECT event_id::text
		FROM platform_replication_inbox
		WHERE applied_at IS NULL AND outcome IN ('received', 'retry')
		  AND available_at <= now()
		ORDER BY received_at, event_id
		LIMIT $1`, limit)
	if err != nil {
		return 0, fmt.Errorf("list pending replication events: %w", err)
	}
	ids := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan pending replication event: %w", err)
		}
		ids = append(ids, id)
	}
	rowsErr := rows.Err()
	rows.Close()
	if rowsErr != nil {
		return 0, fmt.Errorf("iterate pending replication events: %w", rowsErr)
	}

	applied := 0
	for _, id := range ids {
		tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return applied, fmt.Errorf("begin inbox apply transaction: %w", err)
		}
		var raw string
		err = tx.QueryRow(ctx, `
			SELECT payload::text
			FROM platform_replication_inbox
			WHERE event_id = $1::uuid AND applied_at IS NULL
			  AND outcome IN ('received', 'retry') AND available_at <= now()
			FOR UPDATE SKIP LOCKED`, id).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			continue
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			return applied, fmt.Errorf("lock pending replication event: %w", err)
		}
		var event Event
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			_ = tx.Rollback(ctx)
			_, _ = s.db.Exec(ctx, `UPDATE platform_replication_inbox SET outcome='rejected', error_code='invalid_payload' WHERE event_id=$1::uuid`, id)
			continue
		}
		if err := event.Validate(); err != nil {
			_ = tx.Rollback(ctx)
			_, _ = s.db.Exec(ctx, `UPDATE platform_replication_inbox SET outcome='rejected', error_code='invalid_event' WHERE event_id=$1::uuid`, id)
			continue
		}
		if err := apply(ctx, tx, event); err != nil {
			_ = tx.Rollback(ctx)
			_, _ = s.db.Exec(ctx, `UPDATE platform_replication_inbox
			SET outcome='retry', error_code='apply_failed', attempts=attempts+1,
			    available_at=now() + LEAST(3600, (5 * power(2, LEAST(attempts, 10)))::INTEGER) * interval '1 second'
			WHERE event_id=$1::uuid AND applied_at IS NULL`, id)
			return applied, fmt.Errorf("apply queued replication event: %w", err)
		}
		tag, err := tx.Exec(ctx, `
			UPDATE platform_replication_inbox
			SET applied_at=now(), outcome='applied', error_code=NULL
			WHERE event_id=$1::uuid AND applied_at IS NULL`, id)
		if err != nil {
			_ = tx.Rollback(ctx)
			return applied, fmt.Errorf("mark queued event applied: %w", err)
		}
		if tag.RowsAffected() != 1 {
			_ = tx.Rollback(ctx)
			continue
		}
		if err := tx.Commit(ctx); err != nil {
			return applied, fmt.Errorf("commit queued event projection: %w", err)
		}
		applied++
	}
	return applied, nil
}
