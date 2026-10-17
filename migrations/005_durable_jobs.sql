create table if not exists testagram_platform.jobs (
  id uuid primary key default gen_random_uuid(),
  queue text not null check (length(queue) between 1 and 160),
  payload jsonb not null check (jsonb_typeof(payload) = 'object'),
  status text not null default 'queued' check (status in ('queued','running','succeeded','dead')),
  attempts integer not null default 0 check (attempts >= 0),
  max_attempts integer not null default 8 check (max_attempts between 1 and 20),
  run_after timestamptz not null default now(),
  locked_by text,
  locked_at timestamptz,
  idempotency_key text,
  last_error text not null default '',
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  completed_at timestamptz,
  check ((status = 'running') = (locked_by is not null and locked_at is not null))
);

create index if not exists platform_jobs_claim_idx
  on testagram_platform.jobs(queue, run_after, created_at)
  where status = 'queued';
create index if not exists platform_jobs_expired_lease_idx
  on testagram_platform.jobs(queue, locked_at)
  where status = 'running';
create unique index if not exists platform_jobs_idempotency_idx
  on testagram_platform.jobs(queue, idempotency_key)
  where idempotency_key is not null;
create index if not exists platform_jobs_history_idx
  on testagram_platform.jobs(status, updated_at desc);

alter table testagram_platform.jobs enable row level security;
comment on table testagram_platform.jobs is 'Durable PostgreSQL job queue. Delivery is at-least-once; handlers must be idempotent.';
