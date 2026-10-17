create schema if not exists testagram_hosting;

alter table public.vps_workloads add column if not exists storage_bytes bigint not null default 268435456 check (storage_bytes > 0);

create table if not exists testagram_hosting.domains (
  id uuid primary key default gen_random_uuid(),
  slug text not null unique,
  hostname text not null unique,
  target_service text not null,
  status text not null default 'pending_dns' check (status in ('pending_dns','active','disabled')),
  dns_record jsonb not null default '{}'::jsonb,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);
create index if not exists hosting_domains_status_idx on testagram_hosting.domains(status, hostname);

create table if not exists testagram_hosting.deployments (
  id uuid primary key default gen_random_uuid(),
  app_name text not null,
  image text not null,
  image_digest text not null check (image_digest ~ '^sha256:[0-9a-f]{64}$'),
  node_id uuid references public.vps_nodes(id) on delete set null,
  workload_id uuid references public.vps_workloads(id) on delete set null,
  cpu_millis integer not null check (cpu_millis between 50 and 64000),
  memory_bytes bigint not null check (memory_bytes between 67108864 and 68719476736),
  storage_bytes bigint not null default 268435456 check (storage_bytes between 67108864 and 1099511627776),
  public_env jsonb not null default '{}'::jsonb,
  secret_refs jsonb not null default '{}'::jsonb,
  status text not null default 'queued' check (status in ('queued','assigned','running','stopped','failed')),
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);
create index if not exists hosting_deployments_status_idx on testagram_hosting.deployments(status, created_at desc);
create index if not exists hosting_deployments_node_idx on testagram_hosting.deployments(node_id, status);

create table if not exists testagram_hosting.usage_snapshots (
  id bigserial primary key,
  node_id uuid references public.vps_nodes(id) on delete set null,
  cpu_cores integer not null check (cpu_cores >= 0),
  memory_bytes bigint not null check (memory_bytes >= 0),
  storage_bytes bigint not null check (storage_bytes >= 0),
  free_bytes bigint not null check (free_bytes >= 0),
  active_workloads integer not null default 0 check (active_workloads >= 0),
  active_cpu_millis bigint not null default 0 check (active_cpu_millis >= 0),
  active_memory_bytes bigint not null default 0 check (active_memory_bytes >= 0),
  active_storage_bytes bigint not null default 0 check (active_storage_bytes >= 0),
  sampled_at timestamptz not null default now()
);
alter table testagram_hosting.usage_snapshots add column if not exists active_storage_bytes bigint not null default 0 check (active_storage_bytes >= 0);

create index if not exists hosting_usage_node_time_idx on testagram_hosting.usage_snapshots(node_id, sampled_at desc);

create table if not exists testagram_hosting.backup_manifests (
  id uuid primary key default gen_random_uuid(),
  backup_kind text not null check (backup_kind in ('postgres','objects','config','full')),
  storage_uri text not null,
  sha256 text not null check (sha256 ~ '^[0-9a-f]{64}$'),
  size_bytes bigint not null check (size_bytes >= 0),
  source_host text not null,
  status text not null default 'registered' check (status in ('registered','verified','failed')),
  verified_at timestamptz,
  created_at timestamptz not null default now()
);
create index if not exists hosting_backups_time_idx on testagram_hosting.backup_manifests(created_at desc);

create table if not exists testagram_hosting.recovery_drills (
  id uuid primary key default gen_random_uuid(),
  backup_id uuid not null references testagram_hosting.backup_manifests(id),
  target_id text not null,
  restored_sha256 text not null check (restored_sha256 ~ '^[0-9a-f]{64}$'),
  success boolean not null,
  notes text not null default '',
  started_at timestamptz not null,
  completed_at timestamptz not null,
  created_at timestamptz not null default now(),
  check (completed_at >= started_at)
);
create index if not exists hosting_recovery_time_idx on testagram_hosting.recovery_drills(completed_at desc);

alter table testagram_hosting.domains enable row level security;
alter table testagram_hosting.deployments enable row level security;
alter table testagram_hosting.usage_snapshots enable row level security;
alter table testagram_hosting.backup_manifests enable row level security;
alter table testagram_hosting.recovery_drills enable row level security;

comment on schema testagram_hosting is 'Private Testagram hosting control-plane state. Access only through the authenticated control plane/service role.';
