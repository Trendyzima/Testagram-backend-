-- Dual-backend replication foundation.
-- Apply explicitly after reviewing the target database and backup. This migration
-- creates isolated tables and does not alter existing product tables or data.
BEGIN;

CREATE TABLE IF NOT EXISTS platform_replication_outbox (
    event_id UUID PRIMARY KEY,
    origin TEXT NOT NULL CHECK (length(origin) BETWEEN 1 AND 128),
    entity_type TEXT NOT NULL CHECK (length(entity_type) BETWEEN 1 AND 128),
    entity_id TEXT NOT NULL CHECK (length(entity_id) BETWEEN 1 AND 128),
    entity_version BIGINT NOT NULL CHECK (entity_version > 0),
    schema_version INTEGER NOT NULL CHECK (schema_version > 0),
    event_type TEXT NOT NULL CHECK (length(event_type) BETWEEN 1 AND 128),
    occurred_at TIMESTAMPTZ NOT NULL,
    payload JSONB NOT NULL CHECK (octet_length(payload::text) <= 1048576),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_until TIMESTAMPTZ,
    delivered_at TIMESTAMPTZ,
    last_error_code TEXT,
    CONSTRAINT replication_outbox_entity_version_unique
      UNIQUE (origin, entity_type, entity_id, entity_version)
);

CREATE INDEX IF NOT EXISTS platform_replication_outbox_ready_idx
    ON platform_replication_outbox (available_at, created_at)
    WHERE delivered_at IS NULL;

CREATE TABLE IF NOT EXISTS platform_replication_inbox (
    event_id UUID PRIMARY KEY,
    origin TEXT NOT NULL CHECK (length(origin) BETWEEN 1 AND 128),
    entity_type TEXT NOT NULL CHECK (length(entity_type) BETWEEN 1 AND 128),
    entity_id TEXT NOT NULL CHECK (length(entity_id) BETWEEN 1 AND 128),
    entity_version BIGINT NOT NULL CHECK (entity_version > 0),
    schema_version INTEGER NOT NULL CHECK (schema_version > 0),
    event_type TEXT NOT NULL CHECK (length(event_type) BETWEEN 1 AND 128),
    payload_hash BYTEA NOT NULL CHECK (octet_length(payload_hash) = 32),
    payload JSONB NOT NULL CHECK (octet_length(payload::text) <= 1048576),
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    applied_at TIMESTAMPTZ,
    outcome TEXT NOT NULL DEFAULT 'received'
      CHECK (outcome IN ('received', 'applied', 'stale', 'rejected', 'retry')),
    error_code TEXT,
    UNIQUE (origin, entity_type, entity_id, entity_version)
);

CREATE INDEX IF NOT EXISTS platform_replication_inbox_pending_idx
    ON platform_replication_inbox (received_at)
    WHERE applied_at IS NULL AND outcome IN ('received', 'retry');

-- Queue payloads and leases are internal service data, not public SQL API data.
REVOKE ALL ON platform_replication_outbox, platform_replication_inbox FROM PUBLIC;

COMMIT;
