-- Production hardening for the VPS control plane and locally-owned notification queue.
alter table public.vps_workloads
  add column if not exists started_at timestamptz,
  add column if not exists finished_at timestamptz;

create table if not exists public.testagram_notifications (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null,
  title text not null,
  body text not null,
  payload jsonb not null default '{}'::jsonb,
  status text not null default 'queued' check (status in ('queued','delivered','failed')),
  attempts integer not null default 0,
  available_at timestamptz not null default now(),
  delivered_at timestamptz,
  last_error text,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create index if not exists testagram_notifications_ready_idx
  on public.testagram_notifications(status, available_at, created_at);

create table if not exists public.testagram_notification_devices (
  id text primary key,
  user_id uuid not null,
  channel text not null default 'sse',
  address text not null,
  enabled boolean not null default true,
  last_seen_at timestamptz not null default now(),
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create index if not exists testagram_notification_devices_user_idx
  on public.testagram_notification_devices(user_id, enabled, last_seen_at desc);

alter table public.testagram_notifications enable row level security;
alter table public.testagram_notification_devices enable row level security;
