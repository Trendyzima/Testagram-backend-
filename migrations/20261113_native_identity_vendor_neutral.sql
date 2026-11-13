-- Retire the legacy webhook-specific timestamp from the native identity path.
-- Historical migrations remain immutable; this migration makes the live schema
-- provider-neutral and records the actual manual-review timestamp instead.
BEGIN;

ALTER TABLE testagram_identity.identity_applications
  DROP CONSTRAINT IF EXISTS identity_applications_verified_adult_complete_check;

ALTER TABLE testagram_identity.identity_applications
  ADD CONSTRAINT identity_applications_verified_adult_complete_check
  CHECK (
    adult_verified_at IS NULL
    OR (
      adult_verification_result='verified'
      AND adult_verification_session_id IS NOT NULL
      AND evidence_fingerprint_sha256 IS NOT NULL
      AND adult_verification_checked_at IS NOT NULL
    )
  ) NOT VALID;

ALTER TABLE testagram_identity.identity_applications
  DROP COLUMN IF EXISTS adult_verification_webhook_received_at;

COMMENT ON COLUMN testagram_identity.identity_applications.adult_verification_session_id IS
  'Testagram-owned verification session identifier; no third-party provider session is required.';
COMMENT ON COLUMN testagram_identity.identity_applications.adult_verification_checked_at IS
  'Timestamp when Testagram staff completed the native age-assurance review.';

COMMIT;
