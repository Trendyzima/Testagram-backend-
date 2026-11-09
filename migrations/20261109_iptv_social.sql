-- IPTV social interactions are Backend-owned and keyed by canonical Appwrite account IDs.
BEGIN;
CREATE TABLE IF NOT EXISTS testagram_local.iptv_channel_reactions (
  channel_id TEXT NOT NULL CHECK (length(channel_id) BETWEEN 1 AND 256),
  user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY(channel_id,user_id)
);
CREATE INDEX IF NOT EXISTS iptv_channel_reactions_user_idx
  ON testagram_local.iptv_channel_reactions(user_id,created_at DESC);

CREATE TABLE IF NOT EXISTS testagram_local.iptv_channel_comments (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  channel_id TEXT NOT NULL CHECK (length(channel_id) BETWEEN 1 AND 256),
  user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  content TEXT NOT NULL CHECK (length(btrim(content)) BETWEEN 1 AND 280),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ NOT NULL DEFAULT (now()+interval '30 days'),
  deleted_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS iptv_channel_comments_channel_idx
  ON testagram_local.iptv_channel_comments(channel_id,created_at DESC)
  WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS testagram_local.iptv_channel_favorites (
  user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  channel_id TEXT NOT NULL CHECK (length(channel_id) BETWEEN 1 AND 256),
  channel_data JSONB NOT NULL CHECK (jsonb_typeof(channel_data)='object'),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY(user_id,channel_id)
);
CREATE INDEX IF NOT EXISTS iptv_channel_favorites_user_idx
  ON testagram_local.iptv_channel_favorites(user_id,created_at DESC);

CREATE TABLE IF NOT EXISTS testagram_local.iptv_watch_history (
  user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  channel_id TEXT NOT NULL CHECK (length(channel_id) BETWEEN 1 AND 256),
  channel_data JSONB NOT NULL CHECK (jsonb_typeof(channel_data)='object'),
  watched_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY(user_id,channel_id)
);
CREATE INDEX IF NOT EXISTS iptv_watch_history_user_idx
  ON testagram_local.iptv_watch_history(user_id,watched_at DESC);

CREATE TABLE IF NOT EXISTS testagram_local.iptv_channel_reports (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  channel_id TEXT NOT NULL CHECK (length(channel_id) BETWEEN 1 AND 256),
  user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  reason TEXT NOT NULL CHECK (length(btrim(reason)) BETWEEN 1 AND 1000),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS iptv_channel_reports_channel_idx
  ON testagram_local.iptv_channel_reports(channel_id,created_at DESC);

ALTER TABLE testagram_local.iptv_channel_reactions ENABLE ROW LEVEL SECURITY;
ALTER TABLE testagram_local.iptv_channel_comments ENABLE ROW LEVEL SECURITY;
ALTER TABLE testagram_local.iptv_channel_favorites ENABLE ROW LEVEL SECURITY;
ALTER TABLE testagram_local.iptv_watch_history ENABLE ROW LEVEL SECURITY;
ALTER TABLE testagram_local.iptv_channel_reports ENABLE ROW LEVEL SECURITY;
REVOKE ALL ON testagram_local.iptv_channel_reactions,testagram_local.iptv_channel_comments,
  testagram_local.iptv_channel_favorites,testagram_local.iptv_watch_history,
  testagram_local.iptv_channel_reports FROM PUBLIC;
DO $acl$
BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
  EXECUTE 'GRANT SELECT,INSERT,DELETE ON testagram_local.iptv_channel_reactions TO service_role';
  EXECUTE 'GRANT SELECT,INSERT,UPDATE ON testagram_local.iptv_channel_comments TO service_role';
  EXECUTE 'GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.iptv_channel_favorites TO service_role';
  EXECUTE 'GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.iptv_watch_history TO service_role';
  EXECUTE 'GRANT SELECT,INSERT ON testagram_local.iptv_channel_reports TO service_role';
 END IF;
END;
$acl$;
COMMENT ON TABLE testagram_local.iptv_channel_reactions IS 'IPTV channel likes owned by the Testagram Backend.';
COMMENT ON TABLE testagram_local.iptv_channel_comments IS 'IPTV comments with bounded lifetime and owner-only deletion.';
COMMENT ON TABLE testagram_local.iptv_channel_favorites IS 'Private IPTV favorites and safe channel metadata per account.';
COMMENT ON TABLE testagram_local.iptv_watch_history IS 'Private IPTV watch history per account.';
COMMENT ON TABLE testagram_local.iptv_channel_reports IS 'User-submitted IPTV channel reports.';
COMMIT;
