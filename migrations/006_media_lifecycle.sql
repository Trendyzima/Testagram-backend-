-- Extend media state machines for retryable deletion, orphan cleanup and moderation.
-- Safe to run repeatedly against an existing database.
alter table testagram_media.upload_intents
  drop constraint if exists upload_intents_status_check;
alter table testagram_media.upload_intents
  add constraint upload_intents_status_check
  check (status in ('issued','completed','cancelled','expired','cleanup_pending'));

alter table testagram_media.assets
  drop constraint if exists assets_status_check;
alter table testagram_media.assets
  add constraint assets_status_check
  check (status in ('ready','deletion_pending','quarantine_pending','deleted','quarantined'));

alter table testagram_media.assets
  add column if not exists moderation_reason text not null default '',
  add column if not exists moderated_at timestamptz,
  add column if not exists moderated_by text not null default '';

create table if not exists testagram_media.moderation_events (
  id bigserial primary key,
  object_key text not null,
  owner_id text not null,
  moderator_id text not null,
  action text not null check (action in ('quarantine')),
  reason text not null,
  created_at timestamptz not null default now()
);

create index if not exists media_upload_intents_cleanup_idx
  on testagram_media.upload_intents(status, expires_at)
  where status in ('issued','cleanup_pending');
create index if not exists media_assets_deletion_retry_idx
  on testagram_media.assets(status, updated_at)
  where status in ('deletion_pending','quarantine_pending');
create index if not exists media_moderation_events_object_idx
  on testagram_media.moderation_events(object_key, created_at desc);

alter table testagram_media.moderation_events enable row level security;

comment on column testagram_media.upload_intents.status is
  'cleanup_pending is a durable retry state for expired upload objects that have not yet been removed from object storage.';
comment on column testagram_media.assets.status is
  'deletion_pending and quarantine_pending are durable retry states; deleted/quarantined mean object deletion was confirmed by storage.';
comment on table testagram_media.moderation_events is
  'Operator-token authenticated media takedown audit trail; access only through the backend service.';
