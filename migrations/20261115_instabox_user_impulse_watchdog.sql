-- Authenticated user impulses drive bounded, auditable service activation requests.
create schema if not exists testagram_watchdog;

create table if not exists testagram_watchdog.impulses (
  id bigserial primary key,
  idempotency_key text not null unique check (length(idempotency_key) between 16 and 128),
  event_type text not null check (event_type in ('login','feed_open','post_publish','media_upload','media_view','federation_interaction')),
  services text[] not null check (cardinality(services) between 1 and 5),
  status text not null default 'queued' check (status in ('queued','claimed','completed','failed')),
  attempts integer not null default 0 check (attempts >= 0),
  lease_until timestamptz,
  last_error text not null default '',
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);
create index if not exists watchdog_impulses_claim_idx
  on testagram_watchdog.impulses(status, created_at) where status in ('queued','claimed');

create table if not exists testagram_watchdog.service_leases (
  service_name text primary key check (service_name in ('backend','data-centre','cdn','algorithm','federation')),
  active_until timestamptz not null,
  last_impulse_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

alter table testagram_watchdog.impulses enable row level security;
alter table testagram_watchdog.service_leases enable row level security;
comment on schema testagram_watchdog is 'Private authenticated-activity watchdog queue and service activation leases; accessible only to the trusted control plane.';
