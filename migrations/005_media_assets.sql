create schema if not exists testagram_media;

create table if not exists testagram_media.assets (
  object_key text primary key,
  owner_id text not null,
  visibility text not null check (visibility in ('private','public')),
  content_type text not null,
  size_bytes bigint not null check (size_bytes > 0),
  etag text not null default '',
  status text not null default 'ready' check (status in ('ready','deleted','quarantined')),
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  check (object_key ~ '^users/[A-Za-z0-9-]{1,128}/[a-f0-9]{32}$')
);

create index if not exists media_assets_owner_created_idx
  on testagram_media.assets(owner_id, created_at desc);
create index if not exists media_assets_status_created_idx
  on testagram_media.assets(status, created_at desc);

alter table testagram_media.assets enable row level security;
comment on schema testagram_media is 'Media metadata verified against object storage by the API service.';
comment on table testagram_media.assets is 'Object metadata is recorded only after a signed HEAD request verifies the uploaded object.';
