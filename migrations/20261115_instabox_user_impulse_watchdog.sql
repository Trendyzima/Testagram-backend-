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
  service_name text primary key check (service_name in ('cdn','algorithm','federation','worker','notification','video-worker','media')),
  active_until timestamptz not null,
  last_impulse_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

alter table testagram_watchdog.impulses enable row level security;
alter table testagram_watchdog.service_leases enable row level security;
comment on schema testagram_watchdog is 'Private authenticated-activity watchdog queue and service activation leases; accessible only to the trusted control plane.';

-- Bind node-agent claims to an authenticated, enrolled activation-capable node.
alter table testagram_watchdog.impulses
  add column if not exists available_at timestamptz not null default now(),
  add column if not exists claimed_by_node_id uuid references public.vps_nodes(id) on delete set null;
create index if not exists watchdog_impulses_available_idx
  on testagram_watchdog.impulses(status, available_at, created_at)
  where status in ('queued','claimed');


-- The public Backend API is allowed to enqueue only fixed event types through
-- this SECURITY DEFINER function. It cannot read the queue or manipulate leases.
CREATE OR REPLACE FUNCTION testagram_watchdog.enqueue_impulse(p_idempotency_key text, p_event_type text)
RETURNS bigint
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, testagram_watchdog
AS $$
DECLARE
  mapped_services text[];
  inserted_id bigint;
  service text;
BEGIN
  IF p_idempotency_key IS NULL OR length(p_idempotency_key) < 16 OR length(p_idempotency_key) > 128
     OR p_idempotency_key ~ '[[:space:]]' THEN
    RAISE EXCEPTION 'invalid watchdog idempotency key' USING ERRCODE='22023';
  END IF;
  mapped_services := CASE p_event_type
    WHEN 'login' THEN ARRAY['algorithm']::text[]
    WHEN 'feed_open' THEN ARRAY['algorithm']::text[]
    WHEN 'post_publish' THEN ARRAY['worker','notification']::text[]
    WHEN 'media_upload' THEN ARRAY['cdn','media','video-worker']::text[]
    WHEN 'media_view' THEN ARRAY['cdn','media']::text[]
    WHEN 'federation_interaction' THEN ARRAY['federation']::text[]
    ELSE ARRAY[]::text[]
  END;
  IF cardinality(mapped_services)=0 THEN
    RAISE EXCEPTION 'unsupported watchdog event type' USING ERRCODE='22023';
  END IF;
  INSERT INTO testagram_watchdog.impulses(idempotency_key,event_type,services)
  VALUES(p_idempotency_key,p_event_type,mapped_services)
  ON CONFLICT(idempotency_key) DO NOTHING
  RETURNING id INTO inserted_id;
  IF inserted_id IS NOT NULL THEN
    FOREACH service IN ARRAY mapped_services LOOP
      INSERT INTO testagram_watchdog.service_leases(service_name,active_until,last_impulse_at,updated_at)
      VALUES(service,now()+interval '10 minutes',now(),now())
      ON CONFLICT(service_name) DO UPDATE SET
        active_until=greatest(testagram_watchdog.service_leases.active_until,excluded.active_until),
        last_impulse_at=now(),updated_at=now();
    END LOOP;
    RETURN inserted_id;
  END IF;
  IF EXISTS (SELECT 1 FROM testagram_watchdog.impulses
      WHERE idempotency_key=p_idempotency_key AND event_type<>p_event_type) THEN
    RAISE EXCEPTION 'watchdog idempotency key conflicts with another event' USING ERRCODE='23505';
  END IF;
  RETURN 0;
END;
$$;
REVOKE ALL ON FUNCTION testagram_watchdog.enqueue_impulse(text,text) FROM PUBLIC;
DO $watchdog_acl$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    GRANT USAGE ON SCHEMA testagram_watchdog TO testagram_app;
    GRANT EXECUTE ON FUNCTION testagram_watchdog.enqueue_impulse(text,text) TO testagram_app;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
    GRANT USAGE ON SCHEMA testagram_watchdog TO service_role;
    GRANT EXECUTE ON FUNCTION testagram_watchdog.enqueue_impulse(text,text) TO service_role;
  END IF;
END;
$watchdog_acl$;
