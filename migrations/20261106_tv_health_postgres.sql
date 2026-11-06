-- Canonical TV channel health persistence in independent Testagram PostgreSQL.
-- GitHub Actions is the only authorized writer; health probes are executed by
-- the workflow and signed with its short-lived GitHub OIDC token.
BEGIN;

CREATE SCHEMA IF NOT EXISTS testagram_tv;

CREATE TABLE IF NOT EXISTS testagram_tv.channel_health (
  channel_id TEXT PRIMARY KEY CHECK (channel_id ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'),
  url TEXT NOT NULL CHECK (length(url) BETWEEN 1 AND 2048),
  is_online BOOLEAN NOT NULL,
  last_checked_at TIMESTAMPTZ NOT NULL,
  last_online_at TIMESTAMPTZ,
  consecutive_failures INTEGER NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
  consecutive_successes INTEGER NOT NULL DEFAULT 0 CHECK (consecutive_successes >= 0),
  latency_ms INTEGER NOT NULL DEFAULT 0 CHECK (latency_ms BETWEEN 0 AND 30000),
  check_error TEXT NOT NULL DEFAULT '' CHECK (length(check_error) <= 500),
  source TEXT NOT NULL DEFAULT '' CHECK (length(source) <= 128),
  country TEXT NOT NULL DEFAULT '' CHECK (length(country) <= 128),
  group_name TEXT NOT NULL DEFAULT '' CHECK (length(group_name) <= 256),
  priority INTEGER NOT NULL DEFAULT 0 CHECK (priority BETWEEN 0 AND 100000),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS tv_channel_health_online_priority_idx
  ON testagram_tv.channel_health (is_online, priority DESC, last_checked_at DESC);
CREATE INDEX IF NOT EXISTS tv_channel_health_country_group_idx
  ON testagram_tv.channel_health (country, group_name, priority DESC);
CREATE INDEX IF NOT EXISTS tv_channel_health_last_checked_idx
  ON testagram_tv.channel_health (last_checked_at DESC);

REVOKE ALL ON SCHEMA testagram_tv FROM PUBLIC;
REVOKE ALL ON TABLE testagram_tv.channel_health FROM PUBLIC;
DO $grants$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'testagram_app') THEN
    GRANT USAGE ON SCHEMA testagram_tv TO testagram_app;
    GRANT SELECT, INSERT, UPDATE ON testagram_tv.channel_health TO testagram_app;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'service_role') THEN
    GRANT USAGE ON SCHEMA testagram_tv TO service_role;
    GRANT SELECT, INSERT, UPDATE ON testagram_tv.channel_health TO service_role;
  END IF;
END;
$grants$;

COMMENT ON TABLE testagram_tv.channel_health IS
  'Latest verified health result per TV channel; independent PostgreSQL is canonical, not Supabase.';

COMMIT;
