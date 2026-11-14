-- Authoritative TV catalogue is provisioned independently from health observations.
-- The health monitor is read-only against this table and may only persist observations
-- for enabled channel IDs whose stream URL still matches the provisioned catalogue.
BEGIN;

CREATE TABLE IF NOT EXISTS testagram_tv.channel_catalog (
  channel_id TEXT PRIMARY KEY CHECK (channel_id ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'),
  stream_url TEXT NOT NULL CHECK (length(stream_url) BETWEEN 1 AND 2048),
  source TEXT NOT NULL DEFAULT '' CHECK (length(source) <= 128),
  country TEXT NOT NULL DEFAULT '' CHECK (length(country) <= 128),
  group_name TEXT NOT NULL DEFAULT '' CHECK (length(group_name) <= 256),
  priority INTEGER NOT NULL DEFAULT 0 CHECK (priority BETWEEN 0 AND 100000),
  enabled BOOLEAN NOT NULL DEFAULT true,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (stream_url ~* '^https?://')
);

-- One-time compatibility backfill preserves channels already known to the old monitor.
-- New catalogue entries must be provisioned explicitly; health writes never create them.
INSERT INTO testagram_tv.channel_catalog
  (channel_id, stream_url, source, country, group_name, priority)
SELECT channel_id, url, source, country, group_name, priority
FROM testagram_tv.channel_health
ON CONFLICT (channel_id) DO NOTHING;

CREATE INDEX IF NOT EXISTS tv_channel_catalog_enabled_priority_idx
  ON testagram_tv.channel_catalog (enabled, priority DESC, channel_id ASC);

REVOKE ALL ON TABLE testagram_tv.channel_catalog FROM PUBLIC;
DO $grants$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'testagram_app') THEN
    GRANT SELECT ON testagram_tv.channel_catalog TO testagram_app;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'service_role') THEN
    GRANT SELECT ON testagram_tv.channel_catalog TO service_role;
  END IF;
END;
$grants$;

COMMENT ON TABLE testagram_tv.channel_catalog IS
  'Authoritative provisioned TV channel definitions; separate from mutable health observations. Provision with a privileged operator/migration role.';

COMMIT;
