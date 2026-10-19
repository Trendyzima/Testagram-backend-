-- Independent Testagram credentials and recovery tokens.
-- Apply after 20261019_testagram_local_accounts_posts.sql. No Supabase auth call is required.
BEGIN;
CREATE SCHEMA IF NOT EXISTS testagram_auth;
CREATE TABLE IF NOT EXISTS testagram_auth.identities (
  user_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  email TEXT NOT NULL,
  email_normalized TEXT GENERATED ALWAYS AS (lower(btrim(email))) STORED,
  username TEXT NOT NULL CHECK (username ~ '^[A-Za-z0-9_]{3,30}$'),
  password_hash TEXT NOT NULL,
  email_verified_at TIMESTAMPTZ,
  verification_token_hash BYTEA,
  verification_expires_at TIMESTAMPTZ,
  reset_token_hash BYTEA,
  reset_expires_at TIMESTAMPTZ,
  password_changed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  token_version BIGINT NOT NULL DEFAULT 1,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT testagram_auth_email_unique UNIQUE (email_normalized)
);
CREATE UNIQUE INDEX IF NOT EXISTS testagram_auth_username_ci
  ON testagram_auth.identities (lower(username));
CREATE INDEX IF NOT EXISTS testagram_auth_verification_expiry
  ON testagram_auth.identities (verification_expires_at) WHERE email_verified_at IS NULL;
CREATE INDEX IF NOT EXISTS testagram_auth_reset_expiry
  ON testagram_auth.identities (reset_expires_at) WHERE reset_token_hash IS NOT NULL;
CREATE TABLE IF NOT EXISTS testagram_auth.rate_limits (
  action TEXT NOT NULL CHECK (length(action) BETWEEN 1 AND 32),
  bucket_hash TEXT NOT NULL CHECK (length(bucket_hash) = 64),
  window_start TIMESTAMPTZ NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  PRIMARY KEY (action, bucket_hash, window_start)
);
CREATE INDEX IF NOT EXISTS testagram_auth_rate_limits_expiry
  ON testagram_auth.rate_limits (window_start);
COMMIT;
