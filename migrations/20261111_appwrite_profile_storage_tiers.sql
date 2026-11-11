-- Appwrite-first profile image storage with automatic Instabox overflow.
BEGIN;

CREATE SCHEMA IF NOT EXISTS testagram_profile_storage;

CREATE TABLE IF NOT EXISTS testagram_profile_storage.bucket_usage (
  bucket_id TEXT PRIMARY KEY CHECK (length(bucket_id) BETWEEN 1 AND 128),
  appwrite_capacity_bytes BIGINT NOT NULL CHECK (appwrite_capacity_bytes > 0),
  safety_reserve_bytes BIGINT NOT NULL CHECK (safety_reserve_bytes >= 0 AND safety_reserve_bytes < appwrite_capacity_bytes),
  baseline_used_bytes BIGINT NOT NULL DEFAULT 0 CHECK (baseline_used_bytes >= 0),
  tracked_used_bytes BIGINT NOT NULL DEFAULT 0 CHECK (tracked_used_bytes >= 0),
  reserved_bytes BIGINT NOT NULL DEFAULT 0 CHECK (reserved_bytes >= 0),
  overflow_active BOOLEAN NOT NULL DEFAULT FALSE,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS testagram_profile_storage.assets (
  asset_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  owner_id UUID NOT NULL,
  provider TEXT NOT NULL CHECK (provider IN ('appwrite','instabox-vps')),
  bucket_id TEXT NOT NULL,
  object_id TEXT NOT NULL,
  content_type TEXT NOT NULL CHECK (content_type IN ('image/jpeg','image/png','image/webp','image/avif')),
  size_bytes BIGINT NOT NULL CHECK (size_bytes > 0),
  checksum_sha256 TEXT NOT NULL CHECK (checksum_sha256 ~ '^[0-9a-f]{64}$'),
  state TEXT NOT NULL DEFAULT 'ready' CHECK (state IN ('reserved','ready','deleting','deleted')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at TIMESTAMPTZ,
  UNIQUE(provider,bucket_id,object_id)
);

CREATE INDEX IF NOT EXISTS profile_storage_assets_owner_created
  ON testagram_profile_storage.assets(owner_id,created_at DESC) WHERE state='ready';
CREATE INDEX IF NOT EXISTS profile_storage_assets_provider_state
  ON testagram_profile_storage.assets(provider,state,created_at);

ALTER TABLE testagram_profile_storage.bucket_usage ENABLE ROW LEVEL SECURITY;
ALTER TABLE testagram_profile_storage.assets ENABLE ROW LEVEL SECURITY;
REVOKE ALL ON SCHEMA testagram_profile_storage FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA testagram_profile_storage FROM PUBLIC;

DO $acl$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='anon') THEN
    EXECUTE 'REVOKE ALL ON ALL TABLES IN SCHEMA testagram_profile_storage FROM anon';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='authenticated') THEN
    EXECUTE 'REVOKE ALL ON ALL TABLES IN SCHEMA testagram_profile_storage FROM authenticated';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
    EXECUTE 'GRANT USAGE ON SCHEMA testagram_profile_storage TO service_role';
    EXECUTE 'GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA testagram_profile_storage TO service_role';
  END IF;
END;
$acl$;

COMMIT;
