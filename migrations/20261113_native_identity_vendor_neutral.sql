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
COMMENT ON COLUMN testagram_identity.identity_applications.adult_verification_provider IS
  'Native Testagram verification method identifier; no external vendor is required.';
COMMENT ON COLUMN testagram_identity.identity_applications.adult_verification_reference IS
  'Opaque Testagram evidence audit reference for the completed verification decision.';
COMMENT ON COLUMN testagram_identity.identity_applications.adult_verification_result IS
  'Native Testagram age-assurance state: pending, manual_review, verified or failed.';
COMMENT ON COLUMN testagram_identity.identity_applications.adult_verification_mode IS
  'Age-assurance mode requested by Testagram; currently age_only.';
COMMENT ON COLUMN testagram_identity.identity_applications.adult_verification_threshold IS
  'Minimum age threshold required by Testagram registration policy.';

COMMIT;
