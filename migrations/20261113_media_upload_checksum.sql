-- Persist the SHA-256 computed by the bounded Backend upload gateway.
-- The digest is authoritative; client-reported checksums are comparison-only.
BEGIN;
ALTER TABLE testagram_media.upload_intents
  ADD COLUMN IF NOT EXISTS checksum_sha256 TEXT NOT NULL DEFAULT '';
ALTER TABLE testagram_media.assets
  ADD COLUMN IF NOT EXISTS checksum_sha256 TEXT NOT NULL DEFAULT '';
COMMENT ON COLUMN testagram_media.assets.checksum_sha256 IS
  'SHA-256 confirmed by Data Centre primary and replica read-back before Backend marks this asset ready.';
COMMENT ON COLUMN testagram_media.upload_intents.checksum_sha256 IS
  'SHA-256 computed by Backend while streaming the bounded upload; used for Data Centre primary verification and independent replica read-back.';
COMMIT;
