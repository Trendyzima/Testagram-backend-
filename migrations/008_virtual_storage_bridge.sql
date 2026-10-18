-- Virtual storage metadata bridge.
-- Keeps the existing media tables authoritative while recording logical placement
-- and durable provider cleanup work. Safe to apply repeatedly.
begin;

alter table testagram_media.upload_intents
  add column if not exists storage_namespace text not null default 'legacy-media',
  add column if not exists storage_provider text not null default 'default';

alter table testagram_media.assets
  add column if not exists storage_namespace text not null default 'legacy-media',
  add column if not exists storage_provider text not null default 'default',
  add column if not exists checksum_sha256 text not null default '';

alter table testagram_media.upload_intents
  drop constraint if exists upload_intents_storage_namespace_check;
alter table testagram_media.upload_intents
  add constraint upload_intents_storage_namespace_check
  check (storage_namespace ~ '^[a-z][a-z0-9-]{0,62}$');

alter table testagram_media.assets
  drop constraint if exists assets_storage_namespace_check;
alter table testagram_media.assets
  add constraint assets_storage_namespace_check
  check (storage_namespace ~ '^[a-z][a-z0-9-]{0,62}$');

alter table testagram_media.upload_intents
  drop constraint if exists upload_intents_storage_provider_check;
alter table testagram_media.upload_intents
  add constraint upload_intents_storage_provider_check
  check (storage_provider ~ '^[a-z][a-z0-9-]{0,62}$');

alter table testagram_media.assets
  drop constraint if exists assets_storage_provider_check;
alter table testagram_media.assets
  add constraint assets_storage_provider_check
  check (storage_provider ~ '^[a-z][a-z0-9-]{0,62}$');

create index if not exists media_assets_namespace_provider_idx
  on testagram_media.assets(storage_namespace, storage_provider, status);

create index if not exists media_upload_intents_namespace_expiry_idx
  on testagram_media.upload_intents(storage_namespace, storage_provider, status, expires_at);

-- Deletion is a durable state transition. Workers may retry provider deletion
-- without losing the original owner/visibility/namespace context.
create table if not exists testagram_media.storage_deletion_jobs (
  id bigserial primary key,
  object_key text not null,
  owner_id text not null,
  visibility text not null check (visibility in ('private','public')),
  storage_namespace text not null check (storage_namespace ~ '^[a-z][a-z0-9-]{0,62}$'),
  storage_provider text not null check (storage_provider ~ '^[a-z][a-z0-9-]{0,62}$'),
  reason text not null check (reason in ('user_delete','expired_upload','moderation','orphan_cleanup')),
  status text not null default 'pending'
    check (status in ('pending','processing','retry','completed','dead_letter')),
  attempts integer not null default 0 check (attempts >= 0),
  available_at timestamptz not null default now(),
  locked_at timestamptz,
  last_error_code text not null default '',
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  completed_at timestamptz,
  unique (object_key, reason)
);

create index if not exists storage_deletion_jobs_ready_idx
  on testagram_media.storage_deletion_jobs(available_at, id)
  where status in ('pending','retry');

alter table testagram_media.storage_deletion_jobs enable row level security;

comment on column testagram_media.assets.storage_namespace is
  'Logical namespace independent of physical bucket names; legacy rows remain in legacy-media until migrated deliberately.';
comment on column testagram_media.assets.storage_provider is
  'Provider routing identifier; deployment configuration resolves identifiers to server-side credentials and endpoints.';
comment on table testagram_media.storage_deletion_jobs is
  'Durable retry queue for storage deletion. Never store credentials or signed URLs in this table.';

commit;
