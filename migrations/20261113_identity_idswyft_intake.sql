-- Idswyft age-assurance intake and callback state.
-- This migration prepares storage only; it does not enable public registration.
BEGIN;

ALTER TABLE testagram_identity.identity_applications
  ALTER COLUMN evidence_fingerprint_sha256 DROP NOT NULL,
  ADD COLUMN IF NOT EXISTS adult_verification_session_id TEXT,
  ADD COLUMN IF NOT EXISTS adult_verification_threshold SMALLINT NOT NULL DEFAULT 18,
  ADD COLUMN IF NOT EXISTS adult_verification_result TEXT NOT NULL DEFAULT 'pending',
  ADD COLUMN IF NOT EXISTS adult_verification_webhook_received_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS status_access_token_sha256 BYTEA;

DO $constraints$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname='identity_applications_adult_verification_threshold_check'
      AND conrelid='testagram_identity.identity_applications'::regclass
  ) THEN
    ALTER TABLE testagram_identity.identity_applications
      ADD CONSTRAINT identity_applications_adult_verification_threshold_check
      CHECK (adult_verification_threshold BETWEEN 18 AND 99) NOT VALID;
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname='identity_applications_adult_verification_result_check'
      AND conrelid='testagram_identity.identity_applications'::regclass
  ) THEN
    ALTER TABLE testagram_identity.identity_applications
      ADD CONSTRAINT identity_applications_adult_verification_result_check
      CHECK (adult_verification_result IN ('pending','verified','failed','manual_review')) NOT VALID;
  END IF;

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
    WHERE conname='identity_applications_verified_adult_complete_check'
      AND conrelid='testagram_identity.identity_applications'::regclass
  ) THEN
    ALTER TABLE testagram_identity.identity_applications
      ADD CONSTRAINT identity_applications_verified_adult_complete_check
      CHECK (
        adult_verified_at IS NULL
        OR (
          adult_verification_result='verified'
          AND adult_verification_session_id IS NOT NULL
          AND evidence_fingerprint_sha256 IS NOT NULL
          AND adult_verification_webhook_received_at IS NOT NULL
        )
      ) NOT VALID;
  END IF;
END;
$constraints$;

CREATE UNIQUE INDEX IF NOT EXISTS identity_applications_adult_verification_session_idx
  ON testagram_identity.identity_applications(adult_verification_session_id)
  WHERE adult_verification_session_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS identity_applications_status_access_token_idx
  ON testagram_identity.identity_applications(status_access_token_sha256)
  WHERE status_access_token_sha256 IS NOT NULL;

-- Non-login service principal used only as the requester of two-person review
-- actions created by the trusted Backend callback. It has no reviewer grants.
INSERT INTO testagram_auth.staff_roles(appwrite_user_id,role,access_revoked_at)
VALUES ('testagram-identity-service','operations',now())
ON CONFLICT (appwrite_user_id) DO NOTHING;

COMMENT ON COLUMN testagram_identity.identity_applications.adult_verification_session_id IS
  'Idswyft verification session bound to this application; never store the raw session token.';
COMMENT ON COLUMN testagram_identity.identity_applications.adult_verification_threshold IS
  'Minimum age threshold requested from the configured assurance provider; registration requires 18.';
COMMENT ON COLUMN testagram_identity.identity_applications.adult_verification_result IS
  'Server-verified Idswyft terminal result. Only verified may enter the two-person identity review queue.';
COMMENT ON COLUMN testagram_identity.identity_applications.status_access_token_sha256 IS
  'SHA-256 of a high-entropy, application-scoped status bearer token; raw token is returned once and never stored.';

COMMIT;
