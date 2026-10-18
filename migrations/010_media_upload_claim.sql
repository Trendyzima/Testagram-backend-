-- Make virtual-storage upload intents single-use under concurrent and replayed PUTs.
-- Existing issued/completed intents remain compatible; the API claims an intent
-- before reading bytes, marks it uploaded only after the provider accepts them,
-- and the janitor reaps abandoned uploading claims after a bounded grace period.
begin;

alter table testagram_media.upload_intents
  drop constraint if exists upload_intents_status_check;
alter table testagram_media.upload_intents
  add constraint upload_intents_status_check
  check (status in ('issued','uploading','uploaded','completed','cancelled','expired','cleanup_pending'));

create index if not exists media_upload_intents_stale_upload_idx
  on testagram_media.upload_intents(status, updated_at)
  where status = 'uploading';

comment on column testagram_media.upload_intents.status is
  'issued may be claimed once as uploading; uploaded means the bounded gateway stored bytes and completion may verify them; stale uploading rows are cleaned after 2 hours.';

commit;
