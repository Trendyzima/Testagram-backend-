-- FCM device token registration is Backend-owned; browser clients never write token tables.
BEGIN;
CREATE TABLE IF NOT EXISTS testagram_local.push_tokens (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  token TEXT NOT NULL CHECK (length(token) BETWEEN 16 AND 4096),
  platform TEXT NOT NULL CHECK (platform IN ('android','ios','web')),
  provider TEXT NOT NULL CHECK (provider IN ('fcm','webpush')),
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(user_id,token)
);
CREATE INDEX IF NOT EXISTS testagram_local_push_tokens_active_user_idx
  ON testagram_local.push_tokens(user_id,last_seen_at DESC) WHERE enabled=TRUE;
REVOKE ALL ON testagram_local.push_tokens FROM PUBLIC;
DO $grants$
BEGIN
 IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
  GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.push_tokens TO testagram_app;
 END IF;
 IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
  GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.push_tokens TO service_role;
 END IF;
END $grants$;
COMMIT;
