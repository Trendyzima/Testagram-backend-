-- Native Testagram auth cutover foundation.
-- Additive only: does not alter canonical user IDs or fabricate password hashes.
-- Any historical identity reconciliation is deliberately outside this native schema.
BEGIN;

CREATE SCHEMA IF NOT EXISTS testagram_identity;

CREATE SCHEMA IF NOT EXISTS testagram_auth;

-- Shared per-minute authentication and capability throttling buckets.
-- Store only a one-way identity digest; never persist raw email, IP or token values.
CREATE TABLE IF NOT EXISTS testagram_auth.rate_limits (
  action TEXT NOT NULL CHECK (length(action) BETWEEN 2 AND 64),
  bucket_hash TEXT NOT NULL CHECK (length(bucket_hash)=64 AND bucket_hash=lower(bucket_hash)),
  window_start TIMESTAMPTZ NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 1 CHECK (attempts > 0),
  PRIMARY KEY (action,bucket_hash,window_start)
);
CREATE INDEX IF NOT EXISTS testagram_auth_rate_limits_window
  ON testagram_auth.rate_limits(window_start);
ALTER TABLE testagram_auth.identities
  ADD COLUMN IF NOT EXISTS password_reset_required BOOLEAN NOT NULL DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS disabled_at TIMESTAMPTZ;

-- Opaque, one-way hashes only. Access JWTs are short-lived; refresh credentials
-- are stored as hashes and rotated atomically by the API.
CREATE TABLE IF NOT EXISTS testagram_auth.sessions (
  session_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL REFERENCES testagram_auth.identities(user_id) ON DELETE CASCADE,
  refresh_token_hash BYTEA NOT NULL UNIQUE CHECK (octet_length(refresh_token_hash)=32),
  family_id UUID NOT NULL,
  token_version BIGINT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_used_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ NOT NULL,
  revoked_at TIMESTAMPTZ,
  revoke_reason TEXT NOT NULL DEFAULT '',
  replaced_by UUID REFERENCES testagram_auth.sessions(session_id),
  user_agent_hash BYTEA,
  ip_hash BYTEA,
  CHECK (expires_at > created_at),
  CHECK (revoked_at IS NULL OR revoked_at >= created_at)
);
CREATE INDEX IF NOT EXISTS testagram_auth_sessions_user_active
  ON testagram_auth.sessions(user_id, expires_at) WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS testagram_auth_sessions_family
  ON testagram_auth.sessions(family_id, created_at);

-- Password-reset capabilities are single-use, expiring and never stored raw.
CREATE TABLE IF NOT EXISTS testagram_auth.password_reset_tokens (
  token_hash BYTEA PRIMARY KEY CHECK (octet_length(token_hash)=32),
  user_id UUID NOT NULL REFERENCES testagram_auth.identities(user_id) ON DELETE CASCADE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ NOT NULL,
  consumed_at TIMESTAMPTZ,
  request_ip_hash BYTEA,
  CHECK (expires_at > created_at)
);
CREATE INDEX IF NOT EXISTS testagram_auth_reset_tokens_user
  ON testagram_auth.password_reset_tokens(user_id, expires_at);

-- Security events are append-only at the application privilege boundary.
CREATE TABLE IF NOT EXISTS testagram_auth.security_audit (
  event_id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  actor_user_id UUID,
  subject_user_id UUID,
  event_type TEXT NOT NULL CHECK (event_type ~ '^[a-z][a-z0-9_.-]{1,63}$'),
  outcome TEXT NOT NULL CHECK (outcome IN ('success','denied','failure')),
  request_id TEXT,
  ip_hash BYTEA,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (octet_length(metadata::text) <= 4096)
);
CREATE INDEX IF NOT EXISTS testagram_auth_audit_subject_time
  ON testagram_auth.security_audit(subject_user_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS testagram_auth_audit_type_time
  ON testagram_auth.security_audit(event_type, occurred_at DESC);

-- Native staff principals use canonical Testagram UUIDs, not provider IDs.
-- Existing Appwrite-backed staff assignments remain untouched until each
-- principal is explicitly reconciled and approved by two distinct operators.
CREATE TABLE IF NOT EXISTS testagram_auth.staff_principals (
  user_id UUID PRIMARY KEY REFERENCES testagram_auth.identities(user_id) ON DELETE RESTRICT,
  enabled BOOLEAN NOT NULL DEFAULT FALSE,
  mfa_enrolled_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  enabled_at TIMESTAMPTZ,
  enabled_by UUID REFERENCES testagram_auth.identities(user_id)
);
CREATE TABLE IF NOT EXISTS testagram_auth.staff_role_grants (
  user_id UUID NOT NULL REFERENCES testagram_auth.staff_principals(user_id) ON DELETE CASCADE,
  role TEXT NOT NULL CHECK (role ~ '^[a-z][a-z0-9_.-]{1,63}$'),
  granted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  granted_by UUID NOT NULL REFERENCES testagram_auth.identities(user_id),
  revoked_at TIMESTAMPTZ,
  revoked_by UUID REFERENCES testagram_auth.identities(user_id),
  PRIMARY KEY(user_id, role, granted_at)
);
CREATE INDEX IF NOT EXISTS testagram_auth_staff_grants_active
  ON testagram_auth.staff_role_grants(user_id, role) WHERE revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS testagram_auth.staff_approval_events (
  approval_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  subject_user_id UUID NOT NULL REFERENCES testagram_auth.staff_principals(user_id),
  action TEXT NOT NULL CHECK (action IN ('enable_staff','grant_role','revoke_role','disable_staff')),
  role TEXT,
  requested_by UUID NOT NULL REFERENCES testagram_auth.identities(user_id),
  approved_by UUID REFERENCES testagram_auth.identities(user_id),
  requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  approved_at TIMESTAMPTZ,
  applied_at TIMESTAMPTZ,
  CHECK (approved_by IS NULL OR approved_by <> requested_by),
  CHECK (approved_at IS NULL OR approved_at >= requested_at),
  CHECK (applied_at IS NULL OR approved_at IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS testagram_auth_staff_approval_pending
  ON testagram_auth.staff_approval_events(requested_at) WHERE applied_at IS NULL;

-- A controlled import ledger preserves source IDs and canonical UUIDs while
-- making collisions and email-verification discrepancies visible. No automatic
-- email-only account linking is permitted.
CREATE TABLE IF NOT EXISTS testagram_auth.legacy_account_reconciliation (
  source_provider TEXT NOT NULL CHECK (source_provider='appwrite'),
  source_account_id TEXT NOT NULL CHECK (length(source_account_id) BETWEEN 1 AND 128),
  canonical_user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE RESTRICT,
  source_email TEXT NOT NULL,
  source_email_verified BOOLEAN NOT NULL,
  source_username TEXT,
  imported_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  reconciled_by TEXT NOT NULL,
  evidence_reference TEXT NOT NULL,
  PRIMARY KEY(source_provider, source_account_id),
  UNIQUE(source_provider, canonical_user_id),
  CHECK (length(reconciled_by) BETWEEN 1 AND 128),
  CHECK (length(evidence_reference) BETWEEN 1 AND 512)
);
CREATE INDEX IF NOT EXISTS testagram_auth_legacy_reconcile_canonical
  ON testagram_auth.legacy_account_reconciliation(canonical_user_id);

-- Do not grant PUBLIC access to auth/session/audit/staff data.
REVOKE ALL ON ALL TABLES IN SCHEMA testagram_auth FROM PUBLIC;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA testagram_auth FROM PUBLIC;

COMMIT;
