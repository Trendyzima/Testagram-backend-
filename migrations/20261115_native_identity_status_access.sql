-- Application-scoped status bearer digests for Testagram-native identity flows.
-- This is intentionally independent of the retired third-party intake schema.
BEGIN;

ALTER TABLE testagram_identity.identity_applications
  ADD COLUMN IF NOT EXISTS status_access_token_sha256 BYTEA,
  ADD COLUMN IF NOT EXISTS status_access_token_expires_at TIMESTAMPTZ;

DO $constraints$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname='identity_applications_status_access_token_check'
      AND conrelid='testagram_identity.identity_applications'::regclass
  ) THEN
    ALTER TABLE testagram_identity.identity_applications
      ADD CONSTRAINT identity_applications_status_access_token_check
      CHECK (status_access_token_sha256 IS NULL OR octet_length(status_access_token_sha256)=32) NOT VALID;
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname='identity_applications_status_token_expiry_pair_check'
      AND conrelid='testagram_identity.identity_applications'::regclass
  ) THEN
    ALTER TABLE testagram_identity.identity_applications
      ADD CONSTRAINT identity_applications_status_token_expiry_pair_check
      CHECK ((status_access_token_sha256 IS NULL) = (status_access_token_expires_at IS NULL)) NOT VALID;
  END IF;
END;
$constraints$;

CREATE UNIQUE INDEX IF NOT EXISTS identity_applications_status_access_token_idx
  ON testagram_identity.identity_applications(status_access_token_sha256)
  WHERE status_access_token_sha256 IS NOT NULL;

COMMENT ON COLUMN testagram_identity.identity_applications.status_access_token_sha256 IS
  'SHA-256 digest of the high-entropy application-scoped status bearer token; raw token is returned once and never persisted.';
COMMENT ON COLUMN testagram_identity.identity_applications.status_access_token_expires_at IS
  'Expiry of the application-scoped status bearer token. This field belongs to the native identity flow and does not depend on any external verification vendor.';

COMMIT;
