-- Native self-hosted PostgreSQL migration for the Backend transactional
-- ActivityPub outbox. Keep this schema aligned with migrations/neon/002_federation_outbox.sql.
BEGIN;

CREATE SCHEMA IF NOT EXISTS testagram_local;

CREATE TABLE IF NOT EXISTS testagram_local.federation_outbox (
    activity_id TEXT PRIMARY KEY,
    actor_user_id UUID NOT NULL,
    payload JSONB NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'dispatching', 'retry', 'published', 'dead_letter')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    available_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    lease_token TEXT,
    lease_until TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    published_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS federation_outbox_claim_idx
    ON testagram_local.federation_outbox (available_at, created_at)
    WHERE state IN ('pending', 'retry', 'dispatching');

CREATE INDEX IF NOT EXISTS federation_outbox_dead_letter_idx
    ON testagram_local.federation_outbox (created_at)
    WHERE state = 'dead_letter';

COMMIT;
