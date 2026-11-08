-- Discovery metadata for media posts and long-form threads.
BEGIN;
ALTER TABLE testagram_local.posts ADD COLUMN IF NOT EXISTS is_video BOOLEAN NOT NULL DEFAULT FALSE;
CREATE TABLE IF NOT EXISTS testagram_local.threads (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 owner_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
 title TEXT NOT NULL CHECK(length(btrim(title)) BETWEEN 1 AND 240),
 content TEXT NOT NULL DEFAULT '' CHECK(length(content)<=30000),
 is_published BOOLEAN NOT NULL DEFAULT TRUE,
 likes_count BIGINT NOT NULL DEFAULT 0 CHECK(likes_count>=0),
 replies_count BIGINT NOT NULL DEFAULT 0 CHECK(replies_count>=0),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 deleted_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS testagram_threads_discovery_idx ON testagram_local.threads(likes_count DESC,created_at DESC) WHERE is_published AND deleted_at IS NULL;
DO $grants$
BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
  GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.threads TO testagram_app;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
  GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.threads TO service_role;
 END IF;
END;
$grants$;
COMMIT;
