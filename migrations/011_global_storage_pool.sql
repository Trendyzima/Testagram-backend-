-- Testagram global media pool, atomic upload reservations, node health and replica inventory.
-- Logical pool target: 100 GiB. Replicas consume physical node capacity, not extra logical quota.
-- Additive migration: does not move existing objects or change current provider routing.
begin;

create schema if not exists testagram_storage;
create table if not exists testagram_storage.pool (
  pool_id smallint primary key default 1 check (pool_id = 1),
  capacity_bytes bigint not null default 107374182400 check (capacity_bytes > 0),
  used_bytes bigint not null default 0 check (used_bytes >= 0),
  reserved_bytes bigint not null default 0 check (reserved_bytes >= 0),
  updated_at timestamptz not null default now(),
  check (used_bytes + reserved_bytes <= capacity_bytes)
);
insert into testagram_storage.pool(pool_id) values (1) on conflict (pool_id) do nothing;

create table if not exists testagram_storage.user_usage (
  owner_id text primary key check (owner_id ~ '^[A-Za-z0-9-]{1,128}$'),
  quota_bytes bigint not null default 5368709120 check (quota_bytes >= 0),
  used_bytes bigint not null default 0 check (used_bytes >= 0),
  reserved_bytes bigint not null default 0 check (reserved_bytes >= 0),
  updated_at timestamptz not null default now(),
  check (used_bytes + reserved_bytes <= quota_bytes)
);

create table if not exists testagram_storage.upload_reservations (
  object_key text primary key references testagram_media.upload_intents(object_key) on delete cascade,
  owner_id text not null,
  reserved_bytes bigint not null check (reserved_bytes > 0),
  actual_bytes bigint not null default 0 check (actual_bytes >= 0),
  status text not null default 'reserved' check (status in ('reserved','consumed','released')),
  expires_at timestamptz not null,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);
create index if not exists storage_upload_reservations_expiry_idx
  on testagram_storage.upload_reservations(expires_at) where status = 'reserved';

create or replace function testagram_storage.reserve_upload_capacity()
returns trigger language plpgsql as $$
begin
  if new.status not in ('issued','uploading') then return new; end if;
  insert into testagram_storage.user_usage(owner_id) values (new.owner_id)
    on conflict (owner_id) do nothing;

  update testagram_storage.pool
     set reserved_bytes = reserved_bytes + new.max_bytes, updated_at = now()
   where pool_id = 1 and used_bytes + reserved_bytes + new.max_bytes <= capacity_bytes;
  if not found then
    raise exception using errcode = '53100', message = 'TESTAGRAM_GLOBAL_STORAGE_LIMIT';
  end if;

  update testagram_storage.user_usage
     set reserved_bytes = reserved_bytes + new.max_bytes, updated_at = now()
   where owner_id = new.owner_id
     and used_bytes + reserved_bytes + new.max_bytes <= quota_bytes;
  if not found then
    raise exception using errcode = '53100', message = 'TESTAGRAM_USER_STORAGE_LIMIT';
  end if;

  insert into testagram_storage.upload_reservations(object_key, owner_id, reserved_bytes, expires_at)
    values (new.object_key, new.owner_id, new.max_bytes, new.expires_at);
  return new;
end $$;

drop trigger if exists media_upload_reserve_capacity on testagram_media.upload_intents;
create trigger media_upload_reserve_capacity after insert on testagram_media.upload_intents
for each row execute function testagram_storage.reserve_upload_capacity();

create or replace function testagram_storage.release_upload_reservation()
returns trigger language plpgsql as $$
declare r testagram_storage.upload_reservations%rowtype;
begin
  if new.status in ('cancelled','expired') and old.status is distinct from new.status then
    select * into r from testagram_storage.upload_reservations
      where object_key = new.object_key and status = 'reserved' for update;
    if found then
      update testagram_storage.pool set reserved_bytes = greatest(0, reserved_bytes-r.reserved_bytes), updated_at=now() where pool_id=1;
      update testagram_storage.user_usage set reserved_bytes = greatest(0, reserved_bytes-r.reserved_bytes), updated_at=now() where owner_id=r.owner_id;
      update testagram_storage.upload_reservations set status='released', updated_at=now() where object_key=r.object_key;
    end if;
  end if;
  return new;
end $$;
drop trigger if exists media_upload_release_capacity on testagram_media.upload_intents;
create trigger media_upload_release_capacity after update of status on testagram_media.upload_intents
for each row execute function testagram_storage.release_upload_reservation();

create or replace function testagram_storage.account_asset_insert()
returns trigger language plpgsql as $$
declare r testagram_storage.upload_reservations%rowtype;
begin
  select * into r from testagram_storage.upload_reservations
    where object_key=new.object_key and status='reserved' for update;
  if found then
    if new.size_bytes > r.reserved_bytes then
      raise exception using errcode='22003', message='TESTAGRAM_UPLOAD_EXCEEDS_RESERVATION';
    end if;
    update testagram_storage.pool set reserved_bytes=reserved_bytes-r.reserved_bytes, used_bytes=used_bytes+new.size_bytes, updated_at=now() where pool_id=1;
    update testagram_storage.user_usage set reserved_bytes=reserved_bytes-r.reserved_bytes, used_bytes=used_bytes+new.size_bytes, updated_at=now() where owner_id=r.owner_id;
    update testagram_storage.upload_reservations set status='consumed', actual_bytes=new.size_bytes, updated_at=now() where object_key=new.object_key;
  else
    insert into testagram_storage.user_usage(owner_id) values (new.owner_id) on conflict(owner_id) do nothing;
    update testagram_storage.pool set used_bytes=used_bytes+new.size_bytes, updated_at=now()
      where pool_id=1 and used_bytes+reserved_bytes+new.size_bytes<=capacity_bytes;
    if not found then raise exception using errcode='53100', message='TESTAGRAM_GLOBAL_STORAGE_LIMIT'; end if;
    update testagram_storage.user_usage set used_bytes=used_bytes+new.size_bytes, updated_at=now()
      where owner_id=new.owner_id and used_bytes+reserved_bytes+new.size_bytes<=quota_bytes;
    if not found then raise exception using errcode='53100', message='TESTAGRAM_USER_STORAGE_LIMIT'; end if;
  end if;
  return new;
end $$;
drop trigger if exists media_asset_account_insert on testagram_media.assets;
create trigger media_asset_account_insert after insert on testagram_media.assets
for each row when (new.status='ready') execute function testagram_storage.account_asset_insert();

create or replace function testagram_storage.account_asset_delete()
returns trigger language plpgsql as $$
begin
  if old.status not in ('deleted','quarantined') then
    update testagram_storage.pool set used_bytes=greatest(0,used_bytes-old.size_bytes),updated_at=now() where pool_id=1;
    update testagram_storage.user_usage set used_bytes=greatest(0,used_bytes-old.size_bytes),updated_at=now() where owner_id=old.owner_id;
  end if;
  return old;
end $$;
drop trigger if exists media_asset_account_delete on testagram_media.assets;
create trigger media_asset_account_delete after delete on testagram_media.assets
for each row execute function testagram_storage.account_asset_delete();

create or replace function testagram_storage.account_asset_status_change()
returns trigger language plpgsql as $$
begin
  if old.status not in ('deleted','quarantined') and new.status in ('deleted','quarantined') then
    update testagram_storage.pool set used_bytes=greatest(0,used_bytes-old.size_bytes),updated_at=now() where pool_id=1;
    update testagram_storage.user_usage set used_bytes=greatest(0,used_bytes-old.size_bytes),updated_at=now() where owner_id=old.owner_id;
  elsif old.status in ('deleted','quarantined') and new.status not in ('deleted','quarantined') then
    update testagram_storage.pool set used_bytes=used_bytes+new.size_bytes,updated_at=now()
      where pool_id=1 and used_bytes+reserved_bytes+new.size_bytes<=capacity_bytes;
    if not found then raise exception using errcode='53100',message='TESTAGRAM_GLOBAL_STORAGE_LIMIT'; end if;
    update testagram_storage.user_usage set used_bytes=used_bytes+new.size_bytes,updated_at=now()
      where owner_id=new.owner_id and used_bytes+reserved_bytes+new.size_bytes<=quota_bytes;
    if not found then raise exception using errcode='53100',message='TESTAGRAM_USER_STORAGE_LIMIT'; end if;
  elsif old.size_bytes<>new.size_bytes then
    raise exception using errcode='55000',message='Changing asset size requires verified reconciliation';
  end if;
  return new;
end $$;
drop trigger if exists media_asset_account_status_change on testagram_media.assets;
create trigger media_asset_account_status_change after update of status,size_bytes on testagram_media.assets
for each row execute function testagram_storage.account_asset_status_change();

create table if not exists testagram_storage.migration_markers(marker text primary key, applied_at timestamptz not null default now());
do $$
begin
  if not exists(select 1 from testagram_storage.migration_markers where marker='seed-existing-assets-v1') then
    insert into testagram_storage.user_usage(owner_id,used_bytes)
      select owner_id,sum(size_bytes)::bigint from testagram_media.assets where status not in ('deleted','quarantined') group by owner_id
      on conflict(owner_id) do update set used_bytes=excluded.used_bytes, quota_bytes=greatest(testagram_storage.user_usage.quota_bytes, excluded.used_bytes), updated_at=now();
    update testagram_storage.pool set used_bytes=coalesce((select sum(size_bytes)::bigint from testagram_media.assets where status not in ('deleted','quarantined')),0),updated_at=now() where pool_id=1;
    insert into testagram_storage.migration_markers(marker) values('seed-existing-assets-v1');
  end if;
end $$;

create table if not exists testagram_storage.nodes (
  node_id uuid primary key default gen_random_uuid(),
  node_name text not null unique,
  provider_key text not null check(provider_key ~ '^[a-z][a-z0-9-]{0,62}$'),
  endpoint text not null default '',
  region text not null default 'default',
  status text not null default 'pending' check(status in ('pending','online','degraded','offline','disabled')),
  capacity_bytes bigint not null default 0 check(capacity_bytes>=0),
  used_bytes bigint not null default 0 check(used_bytes>=0),
  reserved_bytes bigint not null default 0 check(reserved_bytes>=0),
  min_free_bytes bigint not null default 1073741824 check(min_free_bytes>=0),
  last_heartbeat_at timestamptz,
  last_error_code text not null default '',
  metadata jsonb not null default '{}'::jsonb,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  check(used_bytes+reserved_bytes<=capacity_bytes)
);
create index if not exists storage_nodes_placement_idx on testagram_storage.nodes(status,provider_key,region,last_heartbeat_at);

create table if not exists testagram_storage.replicas (
  object_key text not null references testagram_media.assets(object_key) on delete cascade,
  node_id uuid not null references testagram_storage.nodes(node_id),
  provider_key text not null,
  replica_status text not null default 'pending' check(replica_status in ('pending','copying','verified','stale','failed','deleting')),
  size_bytes bigint not null check(size_bytes>0),
  checksum_sha256 text not null default '',
  verified_at timestamptz,
  last_error_code text not null default '',
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  primary key(object_key,node_id)
);
create index if not exists storage_replicas_health_idx on testagram_storage.replicas(replica_status,updated_at);

create or replace function testagram_storage.expire_abandoned_reservations(p_limit integer default 500)
returns integer language plpgsql as $$
declare v_count integer:=0; r record;
begin
  if p_limit<1 or p_limit>5000 then raise exception 'p_limit must be between 1 and 5000'; end if;
  for r in select object_key from testagram_storage.upload_reservations
    where status='reserved' and expires_at<now() order by expires_at limit p_limit for update skip locked
  loop
    update testagram_media.upload_intents set status='expired',updated_at=now()
      where object_key=r.object_key and status in ('issued','uploading','uploaded');
    v_count:=v_count+1;
  end loop;
  return v_count;
end $$;

create or replace view testagram_storage.pool_status as
select pool_id,capacity_bytes,used_bytes,reserved_bytes,
       capacity_bytes-used_bytes-reserved_bytes as available_bytes,
       round(100.0*used_bytes/nullif(capacity_bytes,0),2) as used_percent,updated_at
from testagram_storage.pool;

alter table testagram_storage.pool enable row level security;
alter table testagram_storage.user_usage enable row level security;
alter table testagram_storage.upload_reservations enable row level security;
alter table testagram_storage.nodes enable row level security;
alter table testagram_storage.replicas enable row level security;
alter table testagram_storage.migration_markers enable row level security;

comment on schema testagram_storage is 'Server-only storage quota, reservations, node health and replica inventory.';
comment on table testagram_storage.pool is 'Single logical media pool. Default capacity is 100 GiB; reservations count against the hard cap.';
comment on table testagram_storage.upload_reservations is 'Atomic per-upload reservation ledger; verified media assets convert reservations to used bytes.';
comment on table testagram_storage.nodes is 'Storage node inventory; offline or low-space nodes must not receive new placements.';
comment on table testagram_storage.replicas is 'Replica inventory; logical quota charged once per asset, physical capacity per replica.';
commit;
