-- Unified Testagram notification event ledger and channel delivery queue.
-- Business transactions publish events in their own PostgreSQL transaction; channel workers
-- consume deliveries independently so provider outages never roll back business operations.
BEGIN;

CREATE TABLE IF NOT EXISTS testagram_local.notification_events (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  recipient_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  actor_id UUID REFERENCES testagram_local.accounts(user_id) ON DELETE SET NULL,
  event_type TEXT NOT NULL CHECK (length(event_type) BETWEEN 3 AND 96),
  event_key TEXT NOT NULL CHECK (length(event_key) BETWEEN 1 AND 256),
  entity_type TEXT NOT NULL DEFAULT '' CHECK (length(entity_type) <= 48),
  entity_id UUID,
  payload JSONB NOT NULL DEFAULT '{}'::jsonb
    CHECK (jsonb_typeof(payload)='object' AND octet_length(payload::text)<=8192),
  occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(recipient_id,event_key)
);
CREATE INDEX IF NOT EXISTS notification_events_recipient_timeline
  ON testagram_local.notification_events(recipient_id,occurred_at DESC,id DESC);

CREATE TABLE IF NOT EXISTS testagram_local.notification_deliveries (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  event_id UUID NOT NULL REFERENCES testagram_local.notification_events(id) ON DELETE CASCADE,
  recipient_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  channel TEXT NOT NULL CHECK(channel IN ('in_app','push','email')),
  status TEXT NOT NULL DEFAULT 'pending'
    CHECK(status IN ('pending','processing','sent','failed','dead','uncertain','cancelled')),
  attempts INTEGER NOT NULL DEFAULT 0 CHECK(attempts>=0),
  available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  lease_owner TEXT,
  lease_expires_at TIMESTAMPTZ,
  provider_message_id TEXT NOT NULL DEFAULT '',
  last_error_code TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(event_id,channel)
);
CREATE INDEX IF NOT EXISTS notification_deliveries_claim
  ON testagram_local.notification_deliveries(channel,available_at,created_at)
  WHERE status IN ('pending','failed');
CREATE INDEX IF NOT EXISTS notification_deliveries_expired_lease
  ON testagram_local.notification_deliveries(lease_expires_at)
  WHERE status='processing';

CREATE TABLE IF NOT EXISTS testagram_local.push_devices (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  platform TEXT NOT NULL CHECK(platform IN ('android','ios')),
  provider TEXT NOT NULL CHECK(provider IN ('fcm','apns')),
  token_hash BYTEA NOT NULL UNIQUE,
  token_ciphertext BYTEA NOT NULL,
  token_nonce BYTEA NOT NULL,
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS push_devices_user_enabled
  ON testagram_local.push_devices(user_id) WHERE enabled=TRUE;

CREATE TABLE IF NOT EXISTS testagram_local.notification_preferences (
  user_id UUID PRIMARY KEY REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  push_social_enabled BOOLEAN NOT NULL DEFAULT TRUE,
  push_wallet_enabled BOOLEAN NOT NULL DEFAULT TRUE,
  push_messages_enabled BOOLEAN NOT NULL DEFAULT TRUE,
  email_wallet_enabled BOOLEAN NOT NULL DEFAULT FALSE,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS testagram_local.notification_reaction_aggregates (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  post_id UUID NOT NULL REFERENCES testagram_local.posts(id) ON DELETE CASCADE,
  recipient_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  window_started_at TIMESTAMPTZ NOT NULL,
  last_reaction_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  reaction_count INTEGER NOT NULL DEFAULT 1 CHECK(reaction_count>0),
  latest_actor_id UUID REFERENCES testagram_local.accounts(user_id) ON DELETE SET NULL,
  actor_ids UUID[] NOT NULL DEFAULT '{}',
  notification_event_id UUID REFERENCES testagram_local.notification_events(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(post_id,recipient_id,window_started_at)
);
CREATE INDEX IF NOT EXISTS notification_reaction_aggregates_pending
  ON testagram_local.notification_reaction_aggregates(last_reaction_at);

REVOKE ALL ON testagram_local.notification_events,testagram_local.notification_deliveries,
  testagram_local.push_devices,testagram_local.notification_preferences,
  testagram_local.notification_reaction_aggregates FROM PUBLIC;

DO $grants$
BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.notification_events,
      testagram_local.notification_deliveries,testagram_local.push_devices,
      testagram_local.notification_preferences,testagram_local.notification_reaction_aggregates TO testagram_app;
  END IF;
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
    GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.notification_events,
      testagram_local.notification_deliveries,testagram_local.push_devices,
      testagram_local.notification_preferences,testagram_local.notification_reaction_aggregates TO service_role;
  END IF;
END;
$grants$;
COMMIT;
