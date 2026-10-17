create schema if not exists testagram_platform;

create table if not exists testagram_platform.feature_flags (
  key text primary key,
  enabled boolean not null default false,
  public boolean not null default false,
  value jsonb not null default '{}'::jsonb,
  version bigint not null default 1,
  updated_at timestamptz not null default now()
);

create table if not exists testagram_platform.events (
  id uuid primary key default gen_random_uuid(),
  event_name text not null,
  user_id uuid,
  session_hash text,
  properties jsonb not null default '{}'::jsonb,
  created_at timestamptz not null default now()
);

create index if not exists platform_events_name_time_idx on testagram_platform.events(event_name, created_at desc);
create index if not exists platform_events_user_time_idx on testagram_platform.events(user_id, created_at desc);

create table if not exists testagram_platform.audit_events (
  id uuid primary key default gen_random_uuid(),
  actor_id uuid,
  action text not null,
  resource_type text not null,
  resource_id text,
  metadata jsonb not null default '{}'::jsonb,
  created_at timestamptz not null default now()
);

create index if not exists platform_audit_time_idx on testagram_platform.audit_events(created_at desc);

alter table testagram_platform.feature_flags enable row level security;
alter table testagram_platform.events enable row level security;
alter table testagram_platform.audit_events enable row level security;
