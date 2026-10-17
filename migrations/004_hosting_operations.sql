create table if not exists testagram_hosting.audit_events (
  id bigserial primary key,
  actor text not null,
  action text not null,
  resource_type text not null,
  resource_id text not null,
  metadata jsonb not null default '{}'::jsonb,
  created_at timestamptz not null default now()
);
create index if not exists hosting_audit_resource_time_idx
  on testagram_hosting.audit_events(resource_type, resource_id, created_at desc);
create index if not exists hosting_audit_time_idx
  on testagram_hosting.audit_events(created_at desc);
alter table testagram_hosting.audit_events enable row level security;
comment on table testagram_hosting.audit_events is 'Append-only-by-application operator audit trail for Testagram hosting actions.';
