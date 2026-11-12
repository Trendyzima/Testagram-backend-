-- Add the profile presentation fields used by the native client.
BEGIN;

ALTER TABLE testagram_local.accounts
  ADD COLUMN IF NOT EXISTS location TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS cover_key TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS social_links JSONB NOT NULL DEFAULT '{}'::jsonb;

ALTER TABLE testagram_local.accounts
  ADD CONSTRAINT testagram_local_accounts_location_length
    CHECK (length(location) <= 120) NOT VALID,
  ADD CONSTRAINT testagram_local_accounts_cover_key_length
    CHECK (length(cover_key) <= 512) NOT VALID,
  ADD CONSTRAINT testagram_local_accounts_social_links_object
    CHECK (jsonb_typeof(social_links) = 'object' AND octet_length(social_links::text) <= 4096) NOT VALID;

ALTER TABLE testagram_local.accounts
  VALIDATE CONSTRAINT testagram_local_accounts_location_length,
  VALIDATE CONSTRAINT testagram_local_accounts_cover_key_length,
  VALIDATE CONSTRAINT testagram_local_accounts_social_links_object;

COMMIT;
