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
    payload JSONB NOT NULL CHECK (octet_length(payload::text) <= 2097152),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_until TIMESTAMPTZ,
    lease_token UUID,
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
    payload JSONB NOT NULL CHECK (octet_length(payload::text) <= 2097152),
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
    ON platform_replication_inbox (available_at, received_at)
    WHERE applied_at IS NULL AND outcome IN ('received', 'retry');

-- Tracks the highest accepted version per source-owned entity. Tombstones prevent
-- a delayed create/update from resurrecting an entity after deletion.
CREATE TABLE IF NOT EXISTS platform_replication_projection (
    origin TEXT NOT NULL,
    entity_type TEXT NOT NULL,
    entity_id TEXT NOT NULL,
    entity_version BIGINT NOT NULL CHECK (entity_version > 0),
    schema_version INTEGER NOT NULL CHECK (schema_version > 0),
    payload JSONB NOT NULL CHECK (octet_length(payload::text) <= 2097152),
    tombstoned BOOLEAN NOT NULL DEFAULT FALSE,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (origin, entity_type, entity_id)
);

CREATE TABLE IF NOT EXISTS platform_replication_entity_versions (
    origin TEXT NOT NULL CHECK (length(origin) BETWEEN 1 AND 128),
    entity_type TEXT NOT NULL CHECK (length(entity_type) BETWEEN 1 AND 128),
    entity_id TEXT NOT NULL CHECK (length(entity_id) BETWEEN 1 AND 128),
    current_version BIGINT NOT NULL CHECK (current_version > 0),
    tombstoned BOOLEAN NOT NULL DEFAULT FALSE,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (origin, entity_type, entity_id)
);

-- Queue payloads and projections are private service data. Revoke grants
-- explicitly when running in Supabase; keep the migration compatible with plain
-- PostgreSQL installations where those predefined roles may not exist.
REVOKE ALL ON platform_replication_outbox, platform_replication_inbox, platform_replication_entity_versions, platform_replication_projection FROM PUBLIC;
DO $replication_acl$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'anon') THEN
    EXECUTE 'REVOKE ALL ON platform_replication_outbox, platform_replication_inbox, platform_replication_entity_versions, platform_replication_projection FROM anon';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'authenticated') THEN
    EXECUTE 'REVOKE ALL ON platform_replication_outbox, platform_replication_inbox, platform_replication_entity_versions, platform_replication_projection FROM authenticated';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'service_role') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON platform_replication_outbox, platform_replication_inbox TO service_role';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON platform_replication_entity_versions, platform_replication_projection TO service_role';
  END IF;
END;
$replication_acl$;

COMMIT;
