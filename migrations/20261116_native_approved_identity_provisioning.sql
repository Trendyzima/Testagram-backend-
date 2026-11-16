-- Native approved-identity provisioning.
-- Additive: preserve historical Appwrite approval/reviewer identifiers.
BEGIN;

ALTER TABLE testagram_identity.identity_approval_tokens
  ADD COLUMN IF NOT EXISTS consumed_by_native_user_id UUID
    REFERENCES testagram_auth.identities(user_id) ON DELETE RESTRICT;

-- The legacy constraint tied consumption exclusively to an Appwrite user ID.
-- Native redemption must record exactly one consumer without corrupting the
-- historical Appwrite consumer column or weakening single-use semantics.
DO $consumption_constraint$
DECLARE
  constraint_name text;
BEGIN
  FOR constraint_name IN
    SELECT conname
    FROM pg_constraint
    WHERE conrelid='testagram_identity.identity_approval_tokens'::regclass
      AND contype='c'
      AND pg_get_constraintdef(oid) LIKE '%consumed_at%'
      AND pg_get_constraintdef(oid) LIKE '%consumed_by_appwrite_user_id%'
  LOOP
    EXECUTE format(
      'ALTER TABLE testagram_identity.identity_approval_tokens DROP CONSTRAINT %I',
      constraint_name
    );
  END LOOP;

  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname='identity_approval_tokens_single_consumer_check'
      AND conrelid='testagram_identity.identity_approval_tokens'::regclass
  ) THEN
    ALTER TABLE testagram_identity.identity_approval_tokens
      ADD CONSTRAINT identity_approval_tokens_single_consumer_check
      CHECK (
        (consumed_at IS NULL
          AND consumed_by_appwrite_user_id IS NULL
          AND consumed_by_native_user_id IS NULL)
        OR
        (consumed_at IS NOT NULL
          AND (
            (consumed_by_appwrite_user_id IS NOT NULL AND consumed_by_native_user_id IS NULL)
            OR
            (consumed_by_appwrite_user_id IS NULL AND consumed_by_native_user_id IS NOT NULL)
          ))
      ) NOT VALID;
  END IF;
END;
$consumption_constraint$;

-- Provisioning and email activation are part of the immutable identity
-- verification audit stream; permit these explicit lifecycle event types.
ALTER TABLE testagram_identity.verification_events
  DROP CONSTRAINT IF EXISTS verification_events_event_type_check;

ALTER TABLE testagram_identity.verification_events
  ADD CONSTRAINT verification_events_event_type_check
  CHECK (event_type IN (
    'session_created','session_started','evidence_submitted','review_requested',
    'decision_verified','decision_failed','decision_escalated','session_expired',
    'session_cancelled','evidence_access_attempted','account_provisioned','email_verified'
  )) NOT VALID;

CREATE TABLE IF NOT EXISTS testagram_identity.native_registration_provisioning (
  application_id UUID PRIMARY KEY REFERENCES testagram_identity.identity_applications(id) ON DELETE RESTRICT,
  user_id UUID NOT NULL UNIQUE REFERENCES testagram_auth.identities(user_id) ON DELETE RESTRICT,
  status TEXT NOT NULL DEFAULT 'verification_pending'
    CHECK (status IN ('verification_pending','active','disabled')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  verified_at TIMESTAMPTZ,
  CHECK ((status='verification_pending' AND verified_at IS NULL) OR
         (status IN ('active','disabled') AND verified_at IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS native_registration_provisioning_status_idx
  ON testagram_identity.native_registration_provisioning(status,updated_at DESC);

REVOKE ALL ON TABLE testagram_identity.native_registration_provisioning FROM PUBLIC;

DO $grants$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    GRANT SELECT,INSERT,UPDATE ON testagram_identity.native_registration_provisioning TO testagram_app;
    GRANT SELECT,UPDATE ON testagram_identity.identity_approval_tokens TO testagram_app;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
    GRANT USAGE ON SCHEMA testagram_identity TO service_role;
    GRANT SELECT,INSERT,UPDATE ON testagram_identity.native_registration_provisioning TO service_role;
    GRANT SELECT,UPDATE ON testagram_identity.identity_approval_tokens TO service_role;
  END IF;
END;
$grants$;

COMMENT ON TABLE testagram_identity.native_registration_provisioning IS
  'One Testagram-native identity per approved application; email verification is a separate activation state.';

COMMIT;