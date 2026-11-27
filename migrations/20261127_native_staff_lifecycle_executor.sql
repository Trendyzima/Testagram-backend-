-- Native Testagram staff lifecycle execution primitives.
-- This migration adds durable invitation state and audit evidence. It does not
-- enable staff provisioning by itself; application handlers must execute each
-- approved action in the same transaction as the approval state transition.
BEGIN;

-- Extend the native approval ledger with explicit staff lifecycle operations.
-- The existing two-person approver trigger and immutable payload digest remain
-- in force for these action types.
ALTER TABLE testagram_auth.native_sensitive_action_requests
  DROP CONSTRAINT IF EXISTS native_sensitive_action_requests_action_type_check;
ALTER TABLE testagram_auth.native_sensitive_action_requests
  ADD CONSTRAINT native_sensitive_action_requests_action_type_check
  CHECK (action_type IN (
    'identity.approve',
    'finance.execute',
    'security.change',
    'deployment.production',
    'staff.invite',
    'staff.role_change',
    'staff.revoke',
    'staff.permission_change',
    'staff.webauthn.enroll'
  ));

CREATE TABLE IF NOT EXISTS testagram_auth.native_staff_invitations (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  request_id UUID NOT NULL UNIQUE
    REFERENCES testagram_auth.native_sensitive_action_requests(id) ON DELETE RESTRICT,
  email_normalized TEXT NOT NULL
    CHECK (email_normalized = lower(btrim(email_normalized))
       AND length(email_normalized) BETWEEN 3 AND 254
       AND position('@' in email_normalized) BETWEEN 2 AND length(email_normalized)-1),
  role TEXT NOT NULL
    CHECK (role IN ('owner','administrator','identity_reviewer','engineer','operations','support','moderator')),
  token_sha256 BYTEA NOT NULL UNIQUE CHECK (octet_length(token_sha256)=32),
  created_by UUID NOT NULL REFERENCES testagram_auth.staff_principals(user_id) ON DELETE RESTRICT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ NOT NULL,
  accepted_by_user_id UUID REFERENCES testagram_auth.identities(user_id) ON DELETE RESTRICT,
  accepted_at TIMESTAMPTZ,
  revoked_at TIMESTAMPTZ,
  CHECK (expires_at > created_at),
  CHECK ((accepted_by_user_id IS NULL) = (accepted_at IS NULL)),
  CHECK (accepted_at IS NULL OR accepted_at <= expires_at),
  CHECK (accepted_at IS NULL OR revoked_at IS NULL)
);
CREATE INDEX IF NOT EXISTS native_staff_invitations_pending_email_idx
  ON testagram_auth.native_staff_invitations(email_normalized, expires_at)
  WHERE accepted_at IS NULL AND revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS testagram_auth.native_staff_lifecycle_events (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  request_id UUID NOT NULL
    REFERENCES testagram_auth.native_sensitive_action_requests(id) ON DELETE RESTRICT,
  actor_user_id UUID NOT NULL REFERENCES testagram_auth.identities(user_id) ON DELETE RESTRICT,
  target_user_id UUID REFERENCES testagram_auth.identities(user_id) ON DELETE RESTRICT,
  target_email TEXT NOT NULL DEFAULT '',
  action TEXT NOT NULL CHECK (action IN (
    'invitation_created',
    'role_granted',
    'role_revoked',
    'staff_disabled',
    'permission_changed',
    'webauthn_enrollment_authorized',
    'invitation_accepted',
    'invitation_revoked'
  )),
  role TEXT NOT NULL DEFAULT '',
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS native_staff_lifecycle_events_target_idx
  ON testagram_auth.native_staff_lifecycle_events(target_user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS native_staff_lifecycle_events_request_idx
  ON testagram_auth.native_staff_lifecycle_events(request_id, created_at);

CREATE OR REPLACE FUNCTION testagram_auth.reject_native_staff_lifecycle_event_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $function$
BEGIN
  RAISE EXCEPTION 'native staff lifecycle events are append-only';
END;
$function$;

DROP TRIGGER IF EXISTS native_staff_lifecycle_events_append_only
  ON testagram_auth.native_staff_lifecycle_events;
CREATE TRIGGER native_staff_lifecycle_events_append_only
  BEFORE UPDATE OR DELETE ON testagram_auth.native_staff_lifecycle_events
  FOR EACH ROW
  EXECUTE FUNCTION testagram_auth.reject_native_staff_lifecycle_event_mutation();

REVOKE ALL ON
  testagram_auth.native_staff_invitations,
  testagram_auth.native_staff_lifecycle_events
FROM PUBLIC;

DO $grants$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    GRANT SELECT, INSERT, UPDATE ON
      testagram_auth.native_staff_invitations,
      testagram_auth.native_staff_lifecycle_events
    TO testagram_app;
    GRANT USAGE, SELECT ON SEQUENCE
      testagram_auth.native_staff_lifecycle_events_id_seq
    TO testagram_app;
  END IF;
END;
$grants$;

COMMENT ON TABLE testagram_auth.native_staff_invitations IS
  'Native Testagram staff invitations. Only a SHA-256 token digest is persisted; plaintext invitation tokens are delivered once and never stored.';
COMMENT ON TABLE testagram_auth.native_staff_lifecycle_events IS
  'Append-only audit events for two-person approved Testagram staff lifecycle changes.';

COMMIT;
