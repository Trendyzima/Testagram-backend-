-- Fresh API media keys bind an Appwrite owner ID and a random 128-bit suffix.
-- Appwrite IDs may contain underscores and periods. Apply the same grammar to
-- upload intents and committed assets so reservation and completion cannot drift.
BEGIN;

ALTER TABLE testagram_media.upload_intents
  DROP CONSTRAINT IF EXISTS upload_intents_object_key_check;
ALTER TABLE testagram_media.upload_intents
  ADD CONSTRAINT upload_intents_object_key_owner_bound_check
  CHECK (object_key ~ '^users/[A-Za-z0-9._-]{1,128}/[a-f0-9]{32}$');

ALTER TABLE testagram_media.assets
  DROP CONSTRAINT IF EXISTS assets_object_key_check;
ALTER TABLE testagram_media.assets
  ADD CONSTRAINT assets_object_key_owner_bound_check
  CHECK (object_key ~ '^users/[A-Za-z0-9._-]{1,128}/[a-f0-9]{32}$');

COMMIT;
