-- Persist a server-computed SHA-256 for each bounded media upload.
-- Clients do not need to buffer large video files in memory to calculate a digest.
ALTER TABLE testagram_media.upload_intents
  ADD COLUMN IF NOT EXISTS checksum_sha256 TEXT NOT NULL DEFAULT '';

COMMENT ON COLUMN testagram_media.upload_intents.checksum_sha256 IS
  'SHA-256 calculated by the Backend while streaming the bounded upload to object storage; used for Data Centre replica verification.';
