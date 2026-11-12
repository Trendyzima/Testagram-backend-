-- Backend-owned canonical relationship state for remote ActivityPub followers.
-- Federation keeps a delivery-routing cache, but this table is authoritative.
BEGIN;

CREATE SCHEMA IF NOT EXISTS testagram_local;

CREATE TABLE IF NOT EXISTS testagram_local.remote_follows (
    local_user_id UUID NOT NULL
        REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
    remote_actor_uri TEXT NOT NULL
        CHECK (
            length(remote_actor_uri) BETWEEN 12 AND 2048
            AND remote_actor_uri LIKE 'https://%'
        ),
    follow_activity_id TEXT NOT NULL
        CHECK (
            length(follow_activity_id) BETWEEN 12 AND 2048
            AND follow_activity_id LIKE 'https://%'
        ),
    last_activity_id TEXT NOT NULL
        CHECK (
            length(last_activity_id) BETWEEN 12 AND 2048
            AND last_activity_id LIKE 'https://%'
        ),
    status TEXT NOT NULL DEFAULT 'accepted'
        CHECK (status IN ('accepted', 'removed')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (local_user_id, remote_actor_uri),
    UNIQUE (follow_activity_id),
    UNIQUE (last_activity_id)
);

CREATE INDEX IF NOT EXISTS testagram_local_remote_follows_active_idx
    ON testagram_local.remote_follows(local_user_id, created_at DESC)
    WHERE status = 'accepted';

CREATE INDEX IF NOT EXISTS testagram_local_remote_follows_actor_idx
    ON testagram_local.remote_follows(remote_actor_uri, updated_at DESC);

REVOKE ALL ON testagram_local.remote_follows FROM PUBLIC;

DO $grants$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    GRANT USAGE ON SCHEMA testagram_local TO testagram_app;
    GRANT SELECT, INSERT, UPDATE, DELETE ON testagram_local.remote_follows TO testagram_app;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
    GRANT USAGE ON SCHEMA testagram_local TO service_role;
    GRANT SELECT, INSERT, UPDATE, DELETE ON testagram_local.remote_follows TO service_role;
  END IF;
END;
$grants$;

COMMIT;
