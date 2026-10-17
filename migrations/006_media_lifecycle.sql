-- Extend media state machines for retryable deletion and orphan cleanup.
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
  check (status in ('ready','deletion_pending','deleted','quarantined'));

create index if not exists media_upload_intents_cleanup_idx
  on testagram_media.upload_intents(status, expires_at)
  where status in ('issued','cleanup_pending');
create index if not exists media_assets_deletion_retry_idx
  on testagram_media.assets(status, updated_at)
  where status='deletion_pending';

comment on column testagram_media.upload_intents.status is
  'cleanup_pending is a durable retry state for expired upload objects that have not yet been removed from object storage.';
comment on column testagram_media.assets.status is
  'deletion_pending is durable retry state; deleted means object deletion was confirmed by storage.';
