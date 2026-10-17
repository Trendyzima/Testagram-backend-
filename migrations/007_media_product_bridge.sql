-- Link self-hosted MinIO metadata to the existing Testagram media_assets UUID.
-- The UUID is created by the Supabase/PostgREST product data plane and is used by
-- post_media.media_asset_id. The object key remains the storage identifier.
alter table testagram_media.assets
  add column if not exists external_media_id uuid,
  add column if not exists original_name text not null default '';

alter table testagram_media.upload_intents
  add column if not exists original_name text not null default '';

create unique index if not exists media_assets_external_media_id_uidx
  on testagram_media.assets(external_media_id)
  where external_media_id is not null;

comment on column testagram_media.assets.external_media_id is
  'UUID of the corresponding product media_assets row, used to maintain post_media joins without exposing storage credentials to clients.';
