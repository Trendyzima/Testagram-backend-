-- Local Testagram users subscribe to external authors without creating
-- native Testagram accounts for ActivityPub actors or AT Protocol DIDs.
BEGIN;

CREATE SCHEMA IF NOT EXISTS testagram_local;

CREATE TABLE IF NOT EXISTS testagram_local.remote_author_subscriptions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    local_user_id UUID NOT NULL
        REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
    protocol TEXT NOT NULL CHECK (protocol IN ('activitypub', 'bluesky')),
    canonical_identity TEXT NOT NULL
        CHECK (length(canonical_identity) BETWEEN 12 AND 2048),
    display_handle TEXT NOT NULL DEFAULT ''
        CHECK (length(display_handle) <= 256),
    verification_status TEXT NOT NULL DEFAULT 'pending'
        CHECK (verification_status IN ('pending', 'verified', 'rejected')),
    subscription_status TEXT NOT NULL DEFAULT 'active'
        CHECK (subscription_status IN ('active', 'removed')),
    verified_at TIMESTAMPTZ,
    last_error_code TEXT NOT NULL DEFAULT ''
        CHECK (length(last_error_code) <= 96),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (local_user_id, protocol, canonical_identity),
    CHECK (
        (protocol = 'activitypub' AND canonical_identity LIKE 'https://%')
        OR
        (protocol = 'bluesky' AND canonical_identity LIKE 'did:%')
    ),
    CHECK (
        (verification_status = 'verified' AND verified_at IS NOT NULL)
        OR
        (verification_status <> 'verified' AND verified_at IS NULL)
    )
);

CREATE INDEX IF NOT EXISTS remote_author_subscriptions_active_user_idx
    ON testagram_local.remote_author_subscriptions(local_user_id, protocol, created_at DESC)
    WHERE subscription_status = 'active' AND verification_status = 'verified';

CREATE INDEX IF NOT EXISTS remote_author_subscriptions_identity_idx
    ON testagram_local.remote_author_subscriptions(protocol, canonical_identity)
    WHERE subscription_status = 'active' AND verification_status = 'verified';

REVOKE ALL ON testagram_local.remote_author_subscriptions FROM PUBLIC;
DO $grants$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    GRANT USAGE ON SCHEMA testagram_local TO testagram_app;
    GRANT SELECT, INSERT, UPDATE, DELETE ON testagram_local.remote_author_subscriptions TO testagram_app;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
    GRANT USAGE ON SCHEMA testagram_local TO service_role;
    GRANT SELECT, INSERT, UPDATE, DELETE ON testagram_local.remote_author_subscriptions TO service_role;
  END IF;
END;
$grants$;

COMMIT;
