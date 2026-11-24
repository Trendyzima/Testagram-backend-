-- Native staff-console authorization and audit ledgers.
-- Legacy provider-keyed rows are left untouched for forensic retention; all
-- new console writes use canonical Testagram UUID identities only.
BEGIN;

-- Native reviewer identity columns replace provider IDs for all new reviews.
-- Keep historical provider columns nullable for audit/reconciliation only.
ALTER TABLE testagram_identity.identity_application_reviews
  ADD COLUMN IF NOT EXISTS reviewer_user_id UUID REFERENCES testagram_auth.staff_principals(user_id);
ALTER TABLE testagram_identity.verification_reviews
  ADD COLUMN IF NOT EXISTS reviewer_user_id UUID REFERENCES testagram_auth.staff_principals(user_id);

-- Historical provider columns are nullable while they exist, but the archive
-- migration later renames them. Do not expect or recreate them on replay.
DO $legacy_reviewer_columns$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='testagram_identity'
      AND table_name='identity_application_reviews'
      AND column_name='reviewer_appwrite_user_id'
  ) THEN
    ALTER TABLE testagram_identity.identity_application_reviews
      ALTER COLUMN reviewer_appwrite_user_id DROP NOT NULL;
  END IF;
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='testagram_identity'
      AND table_name='verification_reviews'
      AND column_name='reviewer_appwrite_user_id'
  ) THEN
    ALTER TABLE testagram_identity.verification_reviews
      ALTER COLUMN reviewer_appwrite_user_id DROP NOT NULL;
  END IF;
END;
$legacy_reviewer_columns$;

CREATE UNIQUE INDEX IF NOT EXISTS identity_application_reviews_native_reviewer_uidx
  ON testagram_identity.identity_application_reviews(application_id,reviewer_user_id)
  WHERE reviewer_user_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS verification_reviews_native_reviewer_uidx
  ON testagram_identity.verification_reviews(session_id,reviewer_user_id)
  WHERE reviewer_user_id IS NOT NULL;


CREATE TABLE IF NOT EXISTS testagram_auth.native_staff_permission_overrides (
  user_id UUID NOT NULL REFERENCES testagram_auth.staff_principals(user_id) ON DELETE CASCADE,
  permission_key TEXT NOT NULL REFERENCES testagram_auth.admin_permissions(permission_key) ON DELETE CASCADE,
  granted BOOLEAN NOT NULL,
  reason TEXT NOT NULL CHECK (length(trim(reason)) BETWEEN 8 AND 500),
  changed_by_user_id UUID NOT NULL REFERENCES testagram_auth.identities(user_id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY(user_id,permission_key)
);

CREATE TABLE IF NOT EXISTS testagram_auth.native_sensitive_action_requests (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  action_type TEXT NOT NULL CHECK (action_type IN ('identity.approve','finance.execute','security.change','deployment.production')),
  requested_by_user_id UUID NOT NULL REFERENCES testagram_auth.staff_principals(user_id),
  target_reference TEXT NOT NULL CHECK (length(trim(target_reference)) BETWEEN 1 AND 200),
  reason TEXT NOT NULL CHECK (length(trim(reason)) BETWEEN 12 AND 1000),
  payload JSONB NOT NULL DEFAULT '{}'::jsonb,
  payload_sha256 CHAR(64) NOT NULL CHECK (payload_sha256 ~ '^[0-9a-f]{64}$'),
  required_approvals SMALLINT NOT NULL DEFAULT 2 CHECK (required_approvals BETWEEN 2 AND 5),
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected','expired','cancelled')),
  expires_at TIMESTAMPTZ NOT NULL DEFAULT (now()+interval '24 hours'),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  decided_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS native_sensitive_action_pending_idx
  ON testagram_auth.native_sensitive_action_requests(status,expires_at,created_at DESC);

CREATE TABLE IF NOT EXISTS testagram_auth.native_sensitive_action_approvals (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  request_id UUID NOT NULL REFERENCES testagram_auth.native_sensitive_action_requests(id) ON DELETE RESTRICT,
  approver_user_id UUID NOT NULL REFERENCES testagram_auth.staff_principals(user_id),
  decision TEXT NOT NULL CHECK (decision IN ('approved','rejected')),
  reason TEXT NOT NULL CHECK (length(trim(reason)) BETWEEN 8 AND 1000),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(request_id,approver_user_id)
);
CREATE INDEX IF NOT EXISTS native_sensitive_action_approvals_request_idx
  ON testagram_auth.native_sensitive_action_approvals(request_id,created_at);

CREATE TABLE IF NOT EXISTS testagram_auth.native_admin_security_audit (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  actor_user_id UUID NOT NULL REFERENCES testagram_auth.identities(user_id),
  action TEXT NOT NULL,
  target_reference TEXT NOT NULL DEFAULT '',
  request_id UUID REFERENCES testagram_auth.native_sensitive_action_requests(id) ON DELETE RESTRICT,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS native_admin_security_audit_created_idx
  ON testagram_auth.native_admin_security_audit(created_at DESC);
CREATE INDEX IF NOT EXISTS native_admin_security_audit_actor_idx
  ON testagram_auth.native_admin_security_audit(actor_user_id,created_at DESC);


REVOKE ALL ON TABLE
  testagram_auth.native_staff_permission_overrides,
  testagram_auth.native_sensitive_action_requests,
  testagram_auth.native_sensitive_action_approvals,
  testagram_auth.native_admin_security_audit
FROM PUBLIC;

DO $grants$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    GRANT SELECT,INSERT,UPDATE ON
      testagram_auth.native_staff_permission_overrides,
      testagram_auth.native_sensitive_action_requests,
      testagram_auth.native_sensitive_action_approvals,
      testagram_auth.native_admin_security_audit
    TO testagram_app;
    GRANT USAGE,SELECT ON SEQUENCE
      testagram_auth.native_sensitive_action_approvals_id_seq,
      testagram_auth.native_admin_security_audit_id_seq
    TO testagram_app;
  END IF;
END;
$grants$;

COMMIT;
