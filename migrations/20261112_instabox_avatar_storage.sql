-- Profile avatars are stored privately in Instabox VPS object storage.
-- testagram_media.upload_intents and testagram_media.assets remain the single
-- source of truth for reservations, quota accounting, checksums and lifecycle.
BEGIN;

ALTER TABLE testagram_local.accounts
  ADD COLUMN IF NOT EXISTS avatar_storage_provider TEXT NOT NULL DEFAULT 'legacy-media';

ALTER TABLE testagram_local.accounts
  DROP CONSTRAINT IF EXISTS accounts_avatar_storage_provider_check;
ALTER TABLE testagram_local.accounts
  ADD CONSTRAINT accounts_avatar_storage_provider_check
  CHECK (avatar_storage_provider IN ('legacy-media','appwrite','instabox-vps'));

COMMENT ON COLUMN testagram_local.accounts.avatar_storage_provider IS
  'Provider for avatar_key. New profile avatars are stored privately in Instabox VPS; appwrite is legacy-only during migration.';

COMMIT;
