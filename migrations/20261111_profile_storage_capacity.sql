-- Appwrite profile-image quota and reservation ledger.
-- This records Appwrite usage independently from general post/video media.
BEGIN;
CREATE SCHEMA IF NOT EXISTS testagram_profile_storage;
CREATE TABLE IF NOT EXISTS testagram_profile_storage.capacity (
  id SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  appwrite_limit_bytes BIGINT NOT NULL CHECK (appwrite_limit_bytes > 0),
  appwrite_used_bytes BIGINT NOT NULL DEFAULT 0 CHECK (appwrite_used_bytes >= 0),
  appwrite_reserved_bytes BIGINT NOT NULL DEFAULT 0 CHECK (appwrite_reserved_bytes >= 0),
  safety_reserve_bytes BIGINT NOT NULL CHECK (safety_reserve_bytes >= 0 AND safety_reserve_bytes < appwrite_limit_bytes),
  reconciled_at TIMESTAMPTZ,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (appwrite_used_bytes <= appwrite_limit_bytes),
  CHECK (appwrite_reserved_bytes <= appwrite_limit_bytes)
);
CREATE TABLE IF NOT EXISTS testagram_profile_storage.objects (
  object_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  owner_id UUID NOT NULL,
  object_key TEXT NOT NULL UNIQUE,
  storage_tier TEXT NOT NULL CHECK (storage_tier IN ('appwrite','instabox-vps')),
  provider_file_id TEXT,
  size_bytes BIGINT NOT NULL CHECK (size_bytes > 0),
  checksum_sha256 TEXT NOT NULL CHECK (checksum_sha256 ~ '^[0-9a-f]{64}$'),
  content_type TEXT NOT NULL CHECK (content_type IN ('image/jpeg','image/png','image/webp','image/avif')),
  state TEXT NOT NULL CHECK (state IN ('reserved','ready','deleting','deleted','failed')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (storage_tier <> 'appwrite' OR provider_file_id IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS profile_storage_objects_owner_idx
  ON testagram_profile_storage.objects(owner_id, state);
REVOKE ALL ON SCHEMA testagram_profile_storage FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA testagram_profile_storage FROM PUBLIC;
DO $grants$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    GRANT USAGE ON SCHEMA testagram_profile_storage TO testagram_app;
    GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA testagram_profile_storage TO testagram_app;
  END IF;
END;
$grants$;
COMMENT ON SCHEMA testagram_profile_storage IS
  'Server-only Appwrite-first profile image capacity and Instabox overflow ledger; never store provider secrets here.';
COMMIT;
