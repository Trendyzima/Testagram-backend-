-- Durable provider replica ledger for the Instabox VPS + Cloudflare R2 + Backblaze B2 media topology.
-- Object bytes remain in private providers; this table stores only placement and verification metadata.
BEGIN;

CREATE TABLE IF NOT EXISTS testagram_media.storage_replicas (
  object_key TEXT NOT NULL REFERENCES testagram_media.assets(object_key) ON DELETE CASCADE,
  provider TEXT NOT NULL CHECK (provider IN ('instabox-vps','cloudflare-r2','backblaze-b2')),
  bucket TEXT NOT NULL CHECK (length(bucket) BETWEEN 1 AND 128),
  object_size_bytes BIGINT NOT NULL CHECK (object_size_bytes > 0),
  checksum_sha256 TEXT NOT NULL CHECK (checksum_sha256 ~ '^[0-9a-f]{64}$'),
  state TEXT NOT NULL CHECK (state IN ('verified','degraded','deleting','deleted')),
  verified_at TIMESTAMPTZ,
  last_error_code TEXT NOT NULL DEFAULT '',
  attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (object_key, provider),
  CHECK ((state = 'verified' AND verified_at IS NOT NULL) OR state <> 'verified')
);

CREATE INDEX IF NOT EXISTS media_storage_replicas_provider_state_idx
  ON testagram_media.storage_replicas(provider, state, updated_at);

ALTER TABLE testagram_media.storage_replicas ENABLE ROW LEVEL SECURITY;
REVOKE ALL ON testagram_media.storage_replicas FROM PUBLIC;

DO $acl$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'anon') THEN
    EXECUTE 'REVOKE ALL ON testagram_media.storage_replicas FROM anon';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'authenticated') THEN
    EXECUTE 'REVOKE ALL ON testagram_media.storage_replicas FROM authenticated';
  END IF;
END;
$acl$;

COMMENT ON TABLE testagram_media.storage_replicas IS
  'Server-only ledger of verified media copies across Instabox VPS, Cloudflare R2 and Backblaze B2. It never stores credentials or signed URLs.';

COMMIT;
