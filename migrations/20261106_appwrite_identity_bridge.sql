-- Appwrite owns authentication; this database owns Testagram's canonical identity.
-- Existing accounts are linked only by a trusted migration/linking operation.
BEGIN;

CREATE SCHEMA IF NOT EXISTS testagram_identity;

CREATE TABLE IF NOT EXISTS testagram_identity.appwrite_accounts (
  appwrite_user_id TEXT PRIMARY KEY CHECK (length(appwrite_user_id) BETWEEN 1 AND 36),
  canonical_user_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
  legacy_user_id UUID UNIQUE,
  email TEXT,
  email_verified BOOLEAN NOT NULL DEFAULT FALSE,
  linked_at TIMESTAMPTZ,
  last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (email IS NULL OR (length(email) <= 254 AND email = lower(btrim(email))))
);

CREATE INDEX IF NOT EXISTS appwrite_accounts_email_idx
  ON testagram_identity.appwrite_accounts(email) WHERE email IS NOT NULL;
CREATE INDEX IF NOT EXISTS appwrite_accounts_legacy_user_idx
  ON testagram_identity.appwrite_accounts(legacy_user_id) WHERE legacy_user_id IS NOT NULL;

REVOKE ALL ON SCHEMA testagram_identity FROM PUBLIC;
REVOKE ALL ON TABLE testagram_identity.appwrite_accounts FROM PUBLIC;

DO $grants$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'testagram_app') THEN
    GRANT USAGE ON SCHEMA testagram_identity TO testagram_app;
    GRANT SELECT, INSERT, UPDATE ON testagram_identity.appwrite_accounts TO testagram_app;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'service_role') THEN
    GRANT USAGE ON SCHEMA testagram_identity TO service_role;
    GRANT SELECT, INSERT, UPDATE ON testagram_identity.appwrite_accounts TO service_role;
  END IF;
END;
$grants$;

COMMENT ON TABLE testagram_identity.appwrite_accounts IS
  'Maps Appwrite Auth identities to stable Testagram UUIDs. Existing users require an explicit verified legacy_user_id link; email equality alone is not proof of ownership.';

COMMIT;
