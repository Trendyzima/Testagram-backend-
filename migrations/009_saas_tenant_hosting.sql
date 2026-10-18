-- Multi-tenant Testagram hosting/storage control-plane foundation.
-- Additive and idempotent. Existing personal Testagram media remains on its
-- existing owner_id path until a separate verified tenant migration is planned.
begin;

create schema if not exists testagram_saas;

create table if not exists testagram_saas.tenants (
  id uuid primary key default gen_random_uuid(),
  slug text not null unique check (slug ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?,
  display_name text not null check (length(trim(display_name)) between 1 and 120),
  tenant_kind text not null default 'organization' check (tenant_kind in ('personal','organization')),
  status text not null default 'provisioning' check (status in ('provisioning','trial','active','suspended','closing','closed')),
  owner_user_id text not null,
  billing_email text not null default '',
  region text not null default 'default',
  metadata jsonb not null default '{}'::jsonb,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  closed_at timestamptz
);
create index if not exists saas_tenants_owner_idx on testagram_saas.tenants(owner_user_id, created_at desc);
create index if not exists saas_tenants_status_idx on testagram_saas.tenants(status, created_at);

create table if not exists testagram_saas.tenant_members (
  tenant_id uuid not null references testagram_saas.tenants(id) on delete cascade,
  user_id text not null,
  role text not null check (role in ('owner','admin','developer','billing','viewer')),
  invited_by text not null default '',
  joined_at timestamptz,
  created_at timestamptz not null default now(),
  primary key (tenant_id, user_id)
);
create index if not exists saas_members_user_idx on testagram_saas.tenant_members(user_id, tenant_id);

create table if not exists testagram_saas.plans (
  id uuid primary key default gen_random_uuid(),
  slug text not null unique,
  display_name text not null,
  status text not null default 'active' check (status in ('active','deprecated','hidden')),
  currency char(3) not null default 'USD',
  monthly_price_minor bigint not null default 0 check (monthly_price_minor >= 0),
  annual_price_minor bigint not null default 0 check (annual_price_minor >= 0),
  entitlements jsonb not null default '{}'::jsonb,
  limits jsonb not null default '{}'::jsonb,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  check (slug ~ '^[a-z0-9][a-z0-9-]{0,62}$')
);

create table if not exists testagram_saas.subscriptions (
  id uuid primary key default gen_random_uuid(),
  tenant_id uuid not null references testagram_saas.tenants(id),
  plan_id uuid not null references testagram_saas.plans(id),
  status text not null default 'pending' check (status in ('pending','trialing','active','past_due','paused','cancelled','expired')),
  billing_provider text not null default 'manual',
  external_customer_ref text not null default '',
  external_subscription_ref text not null default '',
  current_period_start timestamptz,
  current_period_end timestamptz,
  cancel_at_period_end boolean not null default false,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  check (current_period_end is null or current_period_start is null or current_period_end > current_period_start)
);
create index if not exists saas_subscriptions_tenant_idx on testagram_saas.subscriptions(tenant_id, status, created_at desc);
create unique index if not exists saas_subscriptions_external_ref_uidx
  on testagram_saas.subscriptions(billing_provider, external_subscription_ref)
  where external_subscription_ref <> '';

create table if not exists testagram_saas.projects (
  id uuid primary key default gen_random_uuid(),
  tenant_id uuid not null references testagram_saas.tenants(id),
  slug text not null,
  display_name text not null,
  project_kind text not null default 'app' check (project_kind in ('app','database','storage','worker','site','api')),
  status text not null default 'active' check (status in ('active','suspended','deleting','deleted')),
  region text not null default 'default',
  created_by text not null,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  unique (tenant_id, slug),
  check (slug ~ '^[a-z0-9][a-z0-9-]{0,62}$')
);
create index if not exists saas_projects_tenant_idx on testagram_saas.projects(tenant_id, status, created_at desc);

-- A resource is an independently metered product instance (compute workload,
-- storage allocation, database, site, worker, or future managed service).
create table if not exists testagram_saas.resources (
  id uuid primary key default gen_random_uuid(),
  tenant_id uuid not null references testagram_saas.tenants(id),
  project_id uuid references testagram_saas.projects(id),
  resource_kind text not null check (resource_kind in ('compute','storage','database','bandwidth','site','worker','backup')),
  service_key text not null,
  status text not null default 'provisioning' check (status in ('provisioning','ready','degraded','suspended','deleting','deleted','failed')),
  region text not null default 'default',
  cpu_millis integer not null default 0 check (cpu_millis >= 0),
  memory_bytes bigint not null default 0 check (memory_bytes >= 0),
  storage_bytes bigint not null default 0 check (storage_bytes >= 0),
  bandwidth_bytes bigint not null default 0 check (bandwidth_bytes >= 0),
  provider_key text not null default 'self-hosted',
  provider_resource_ref text not null default '',
  config jsonb not null default '{}'::jsonb,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  deleted_at timestamptz
);
create index if not exists saas_resources_tenant_idx on testagram_saas.resources(tenant_id, status, resource_kind);
create index if not exists saas_resources_project_idx on testagram_saas.resources(project_id, status);

-- Atomic reservation counters let API instances reject quota overflow before
-- provisioning. Update these rows transactionally with resource reservations.
create table if not exists testagram_saas.quota_counters (
  tenant_id uuid not null references testagram_saas.tenants(id) on delete cascade,
  quota_key text not null,
  hard_limit bigint not null check (hard_limit >= 0),
  reserved bigint not null default 0 check (reserved >= 0),
  consumed bigint not null default 0 check (consumed >= 0),
  updated_at timestamptz not null default now(),
  primary key (tenant_id, quota_key),
  check (reserved + consumed <= hard_limit)
);

create table if not exists testagram_saas.resource_reservations (
  id uuid primary key default gen_random_uuid(),
  tenant_id uuid not null references testagram_saas.tenants(id),
  resource_id uuid references testagram_saas.resources(id),
  quota_key text not null,
  quantity bigint not null check (quantity > 0),
  status text not null default 'reserved' check (status in ('reserved','consumed','released','expired')),
  idempotency_key text not null,
  expires_at timestamptz,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  unique (tenant_id, idempotency_key)
);
create index if not exists saas_reservations_expiry_idx on testagram_saas.resource_reservations(status, expires_at)
  where status = 'reserved';

-- Append-only metering ledger; the source event id is unique so retries cannot
-- double charge a tenant. Amounts are usage quantities, not currency amounts.
create table if not exists testagram_saas.usage_events (
  id bigserial primary key,
  tenant_id uuid not null references testagram_saas.tenants(id),
  resource_id uuid references testagram_saas.resources(id),
  meter_key text not null,
  quantity bigint not null check (quantity >= 0),
  unit text not null,
  source text not null,
  source_event_id text not null,
  occurred_at timestamptz not null,
  dimensions jsonb not null default '{}'::jsonb,
  created_at timestamptz not null default now(),
  unique (source, source_event_id)
);
create index if not exists saas_usage_tenant_time_idx on testagram_saas.usage_events(tenant_id, occurred_at desc);
create index if not exists saas_usage_meter_time_idx on testagram_saas.usage_events(meter_key, occurred_at desc);

-- Webhook inbox gives payment providers a durable idempotency boundary.
-- Verify signatures in the HTTP handler before inserting an event here.
create table if not exists testagram_saas.billing_events (
  id bigserial primary key,
  provider text not null,
  provider_event_id text not null,
  event_type text not null,
  tenant_id uuid references testagram_saas.tenants(id),
  status text not null default 'received' check (status in ('received','processing','processed','retry','dead_letter')),
  attempt_count integer not null default 0 check (attempt_count >= 0),
  payload jsonb not null,
  last_error_code text not null default '',
  received_at timestamptz not null default now(),
  processed_at timestamptz,
  unique (provider, provider_event_id)
);
create index if not exists saas_billing_events_ready_idx on testagram_saas.billing_events(status, received_at)
  where status in ('received','retry');

create table if not exists testagram_saas.audit_events (
  id bigserial primary key,
  tenant_id uuid references testagram_saas.tenants(id),
  actor_user_id text not null,
  action text not null,
  resource_kind text not null,
  resource_id text not null,
  request_id text not null default '',
  metadata jsonb not null default '{}'::jsonb,
  created_at timestamptz not null default now()
);
create index if not exists saas_audit_tenant_time_idx on testagram_saas.audit_events(tenant_id, created_at desc);

-- These tables are backend-only. No user-facing SQL policies are created here;
-- direct client access must remain denied until explicit membership-aware RLS
-- policies and negative cross-tenant tests are added. Backend service role access
-- must still enforce tenant_id in every query.
do $$
declare
  table_name text;
begin
  foreach table_name in array array[
    'tenants','tenant_members','plans','subscriptions','projects','resources',
    'quota_counters','resource_reservations','usage_events','billing_events','audit_events'
  ] loop
    execute format('alter table testagram_saas.%I enable row level security', table_name);
  end loop;
end $$;

comment on schema testagram_saas is
  'Backend-only multi-tenant control plane for Testagram app hosting, managed storage and future MVP hosting rentals.';
comment on table testagram_saas.quota_counters is
  'Transactional quota ledger. Provisioning must reserve capacity here before external side effects.';
comment on table testagram_saas.usage_events is
  'Idempotent append-only metering facts. Billing calculations derive from these events and plan entitlements.';
comment on table testagram_saas.billing_events is
  'Verified billing-provider webhook inbox. Never insert unverified webhook payloads as trusted events.';

commit;
),
  display_name text not null check (length(trim(display_name)) between 1 and 120),
  tenant_kind text not null default 'organization' check (tenant_kind in ('personal','organization')),
  status text not null default 'provisioning' check (status in ('provisioning','trial','active','suspended','closing','closed')),
  owner_user_id text not null,
  billing_email text not null default '',
  region text not null default 'default',
  metadata jsonb not null default '{}'::jsonb,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  closed_at timestamptz
);
create index if not exists saas_tenants_owner_idx on testagram_saas.tenants(owner_user_id, created_at desc);
create index if not exists saas_tenants_status_idx on testagram_saas.tenants(status, created_at);

create table if not exists testagram_saas.tenant_members (
  tenant_id uuid not null references testagram_saas.tenants(id) on delete cascade,
  user_id text not null,
  role text not null check (role in ('owner','admin','developer','billing','viewer')),
  invited_by text not null default '',
  joined_at timestamptz,
  created_at timestamptz not null default now(),
  primary key (tenant_id, user_id)
);
create index if not exists saas_members_user_idx on testagram_saas.tenant_members(user_id, tenant_id);

create table if not exists testagram_saas.plans (
  id uuid primary key default gen_random_uuid(),
  slug text not null unique,
  display_name text not null,
  status text not null default 'active' check (status in ('active','deprecated','hidden')),
  currency char(3) not null default 'USD',
  monthly_price_minor bigint not null default 0 check (monthly_price_minor >= 0),
  annual_price_minor bigint not null default 0 check (annual_price_minor >= 0),
  entitlements jsonb not null default '{}'::jsonb,
  limits jsonb not null default '{}'::jsonb,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  check (slug ~ '^[a-z0-9][a-z0-9-]{0,62}$')
);

create table if not exists testagram_saas.subscriptions (
  id uuid primary key default gen_random_uuid(),
  tenant_id uuid not null references testagram_saas.tenants(id),
  plan_id uuid not null references testagram_saas.plans(id),
  status text not null default 'pending' check (status in ('pending','trialing','active','past_due','paused','cancelled','expired')),
  billing_provider text not null default 'manual',
  external_customer_ref text not null default '',
  external_subscription_ref text not null default '',
  current_period_start timestamptz,
  current_period_end timestamptz,
  cancel_at_period_end boolean not null default false,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  unique (billing_provider, external_subscription_ref),
  check (current_period_end is null or current_period_start is null or current_period_end > current_period_start)
);
create index if not exists saas_subscriptions_tenant_idx on testagram_saas.subscriptions(tenant_id, status, created_at desc);

create table if not exists testagram_saas.projects (
  id uuid primary key default gen_random_uuid(),
  tenant_id uuid not null references testagram_saas.tenants(id),
  slug text not null,
  display_name text not null,
  project_kind text not null default 'app' check (project_kind in ('app','database','storage','worker','site','api')),
  status text not null default 'active' check (status in ('active','suspended','deleting','deleted')),
  region text not null default 'default',
  created_by text not null,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  unique (tenant_id, slug),
  check (slug ~ '^[a-z0-9][a-z0-9-]{0,62}$')
);
create index if not exists saas_projects_tenant_idx on testagram_saas.projects(tenant_id, status, created_at desc);

-- A resource is an independently metered product instance (compute workload,
-- storage allocation, database, site, worker, or future managed service).
create table if not exists testagram_saas.resources (
  id uuid primary key default gen_random_uuid(),
  tenant_id uuid not null references testagram_saas.tenants(id),
  project_id uuid references testagram_saas.projects(id),
  resource_kind text not null check (resource_kind in ('compute','storage','database','bandwidth','site','worker','backup')),
  service_key text not null,
  status text not null default 'provisioning' check (status in ('provisioning','ready','degraded','suspended','deleting','deleted','failed')),
  region text not null default 'default',
  cpu_millis integer not null default 0 check (cpu_millis >= 0),
  memory_bytes bigint not null default 0 check (memory_bytes >= 0),
  storage_bytes bigint not null default 0 check (storage_bytes >= 0),
  bandwidth_bytes bigint not null default 0 check (bandwidth_bytes >= 0),
  provider_key text not null default 'self-hosted',
  provider_resource_ref text not null default '',
  config jsonb not null default '{}'::jsonb,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  deleted_at timestamptz
);
create index if not exists saas_resources_tenant_idx on testagram_saas.resources(tenant_id, status, resource_kind);
create index if not exists saas_resources_project_idx on testagram_saas.resources(project_id, status);

-- Atomic reservation counters let API instances reject quota overflow before
-- provisioning. Update these rows transactionally with resource reservations.
create table if not exists testagram_saas.quota_counters (
  tenant_id uuid not null references testagram_saas.tenants(id) on delete cascade,
  quota_key text not null,
  hard_limit bigint not null check (hard_limit >= 0),
  reserved bigint not null default 0 check (reserved >= 0),
  consumed bigint not null default 0 check (consumed >= 0),
  updated_at timestamptz not null default now(),
  primary key (tenant_id, quota_key),
  check (reserved + consumed <= hard_limit)
);

create table if not exists testagram_saas.resource_reservations (
  id uuid primary key default gen_random_uuid(),
  tenant_id uuid not null references testagram_saas.tenants(id),
  resource_id uuid references testagram_saas.resources(id),
  quota_key text not null,
  quantity bigint not null check (quantity > 0),
  status text not null default 'reserved' check (status in ('reserved','consumed','released','expired')),
  idempotency_key text not null,
  expires_at timestamptz,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  unique (tenant_id, idempotency_key)
);
create index if not exists saas_reservations_expiry_idx on testagram_saas.resource_reservations(status, expires_at)
  where status = 'reserved';

-- Append-only metering ledger; the source event id is unique so retries cannot
-- double charge a tenant. Amounts are usage quantities, not currency amounts.
create table if not exists testagram_saas.usage_events (
  id bigserial primary key,
  tenant_id uuid not null references testagram_saas.tenants(id),
  resource_id uuid references testagram_saas.resources(id),
  meter_key text not null,
  quantity bigint not null check (quantity >= 0),
  unit text not null,
  source text not null,
  source_event_id text not null,
  occurred_at timestamptz not null,
  dimensions jsonb not null default '{}'::jsonb,
  created_at timestamptz not null default now(),
  unique (source, source_event_id)
);
create index if not exists saas_usage_tenant_time_idx on testagram_saas.usage_events(tenant_id, occurred_at desc);
create index if not exists saas_usage_meter_time_idx on testagram_saas.usage_events(meter_key, occurred_at desc);

-- Webhook inbox gives payment providers a durable idempotency boundary.
-- Verify signatures in the HTTP handler before inserting an event here.
create table if not exists testagram_saas.billing_events (
  id bigserial primary key,
  provider text not null,
  provider_event_id text not null,
  event_type text not null,
  tenant_id uuid references testagram_saas.tenants(id),
  status text not null default 'received' check (status in ('received','processing','processed','retry','dead_letter')),
  attempt_count integer not null default 0 check (attempt_count >= 0),
  payload jsonb not null,
  last_error_code text not null default '',
  received_at timestamptz not null default now(),
  processed_at timestamptz,
  unique (provider, provider_event_id)
);
create index if not exists saas_billing_events_ready_idx on testagram_saas.billing_events(status, received_at)
  where status in ('received','retry');

create table if not exists testagram_saas.audit_events (
  id bigserial primary key,
  tenant_id uuid references testagram_saas.tenants(id),
  actor_user_id text not null,
  action text not null,
  resource_kind text not null,
  resource_id text not null,
  request_id text not null default '',
  metadata jsonb not null default '{}'::jsonb,
  created_at timestamptz not null default now()
);
create index if not exists saas_audit_tenant_time_idx on testagram_saas.audit_events(tenant_id, created_at desc);

-- These tables are backend-only. No user-facing SQL policies are created here;
-- direct client access must remain denied until explicit membership-aware RLS
-- policies and negative cross-tenant tests are added. Backend service role access
-- must still enforce tenant_id in every query.
do $$
declare
  table_name text;
begin
  foreach table_name in array array[
    'tenants','tenant_members','plans','subscriptions','projects','resources',
    'quota_counters','resource_reservations','usage_events','billing_events','audit_events'
  ] loop
    execute format('alter table testagram_saas.%I enable row level security', table_name);
  end loop;
end $$;

comment on schema testagram_saas is
  'Backend-only multi-tenant control plane for Testagram app hosting, managed storage and future MVP hosting rentals.';
comment on table testagram_saas.quota_counters is
  'Transactional quota ledger. Provisioning must reserve capacity here before external side effects.';
comment on table testagram_saas.usage_events is
  'Idempotent append-only metering facts. Billing calculations derive from these events and plan entitlements.';
comment on table testagram_saas.billing_events is
  'Verified billing-provider webhook inbox. Never insert unverified webhook payloads as trusted events.';

commit;
