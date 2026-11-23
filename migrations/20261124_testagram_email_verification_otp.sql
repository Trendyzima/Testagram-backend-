-- Add a short-lived, single-use email OTP alongside the existing deep link.
-- Raw codes are never stored; only SHA-256 hashes are persisted.
BEGIN;

ALTER TABLE testagram_auth.identities
  ADD COLUMN IF NOT EXISTS verification_otp_hash BYTEA,
  ADD COLUMN IF NOT EXISTS verification_otp_expires_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS verification_otp_attempts INTEGER NOT NULL DEFAULT 0;

DO $constraints$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid='testagram_auth.identities'::regclass
      AND conname='testagram_auth_verification_otp_hash_length'
  ) THEN
    ALTER TABLE testagram_auth.identities
      ADD CONSTRAINT testagram_auth_verification_otp_hash_length
      CHECK (verification_otp_hash IS NULL OR octet_length(verification_otp_hash)=32);
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid='testagram_auth.identities'::regclass
      AND conname='testagram_auth_verification_otp_attempts'
  ) THEN
    ALTER TABLE testagram_auth.identities
      ADD CONSTRAINT testagram_auth_verification_otp_attempts
      CHECK (verification_otp_attempts BETWEEN 0 AND 5);
  END IF;
END;
$constraints$;

CREATE INDEX IF NOT EXISTS testagram_auth_verification_otp_expiry
  ON testagram_auth.identities(verification_otp_expires_at)
  WHERE email_verified_at IS NULL AND verification_otp_hash IS NOT NULL;

COMMIT;
