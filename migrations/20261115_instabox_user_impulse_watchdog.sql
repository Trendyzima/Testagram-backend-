-- Durable authenticated-user activity queue for the InstaBox watchdog.
CREATE SCHEMA IF NOT EXISTS testagram_watchdog;

CREATE TABLE IF NOT EXISTS testagram_watchdog.impulses (
  id bigserial PRIMARY KEY,
  idempotency_key text NOT NULL UNIQUE CHECK (length(idempotency_key) BETWEEN 16 AND 128),
  event_type text NOT NULL CHECK (event_type IN ('login','feed_open','post_publish','media_upload','media_view','federation_interaction')),
  services text[] NOT NULL CHECK (cardinality(services) BETWEEN 1 AND 5),
  status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','claimed','completed','failed')),
  attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  available_at timestamptz NOT NULL DEFAULT now(),
  lease_until timestamptz,
  last_error text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS watchdog_impulses_claim_idx
  ON testagram_watchdog.impulses(status,available_at,created_at)
  WHERE status IN ('queued','claimed');

CREATE TABLE IF NOT EXISTS testagram_watchdog.service_leases (
  service_name text PRIMARY KEY CHECK (service_name IN ('cdn','algorithm','federation','worker','notification','video-worker','media')),
  active_until timestamptz NOT NULL,
  last_impulse_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE testagram_watchdog.impulses ENABLE ROW LEVEL SECURITY;
ALTER TABLE testagram_watchdog.service_leases ENABLE ROW LEVEL SECURITY;
REVOKE ALL ON testagram_watchdog.impulses,testagram_watchdog.service_leases FROM PUBLIC;
COMMENT ON SCHEMA testagram_watchdog IS 'Private activity impulse queue and service activation leases; accessed only by the trusted control plane.';

-- Backend API can enqueue only through the scoped HTTP ingress token. It has
-- no SQL grants to inspect or mutate queue rows.
