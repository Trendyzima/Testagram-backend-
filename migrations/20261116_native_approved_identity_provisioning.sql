-- Native approved-identity provisioning.
-- Additive: preserve historical Appwrite approval/reviewer identifiers.
BEGIN;

ALTER TABLE testagram_identity.identity_approval_tokens
  ADD COLUMN IF NOT EXISTS consumed_by_native_user_id UUID
    REFERENCES testagram_auth.identities(user_id) ON DELETE RESTRICT;

CREATE TABLE IF NOT EXISTS testagram_identity.native_registration_provisioning (
  application_id UUID PRIMARY KEY REFERENCES testagram_identity.identity_applications(id) ON DELETE RESTRICT,
  user_id UUID NOT NULL UNIQUE REFERENCES testagram_auth.identities(user_id) ON DELETE RESTRICT,
  status TEXT NOT NULL DEFAULT 'verification_pending'
    CHECK (status IN ('verification_pending','active','disabled')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  verified_at TIMESTAMPTZ,
  CHECK ((status='active') = (verified_at IS NOT NULL))
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