-- Keep the short-lived client-upload capability separate from the sealed
-- evidence object consumed by reviewers. Presigned PUT URLs can be reused until
-- expiry, so the reviewed object must never be the writable staging key.
BEGIN;

ALTER TABLE testagram_identity.verification_evidence
  ADD COLUMN IF NOT EXISTS staging_object_key TEXT;

UPDATE testagram_identity.verification_evidence
SET staging_object_key=object_key
WHERE staging_object_key IS NULL AND status='upload_pending';

CREATE UNIQUE INDEX IF NOT EXISTS verification_evidence_staging_object_key_uidx
  ON testagram_identity.verification_evidence(staging_object_key)
  WHERE staging_object_key IS NOT NULL;

COMMENT ON COLUMN testagram_identity.verification_evidence.object_key IS
  'Sealed private evidence object key used by reviewers after checksum-verified promotion.';
COMMENT ON COLUMN testagram_identity.verification_evidence.staging_object_key IS
  'Short-lived applicant upload target. Never expose this key to reviewers; the writable presigned URL cannot mutate the sealed object.';

COMMIT;
