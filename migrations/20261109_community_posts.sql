-- Attach canonical local posts to Backend-owned communities.
BEGIN;
ALTER TABLE testagram_local.posts
  ADD COLUMN IF NOT EXISTS community_id UUID REFERENCES testagram_local.communities(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS testagram_local_community_posts_created_idx
  ON testagram_local.posts(community_id,created_at DESC,id DESC)
  WHERE community_id IS NOT NULL AND deleted_at IS NULL;
DO $grants$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    GRANT SELECT,INSERT,UPDATE ON testagram_local.posts TO testagram_app;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
    GRANT SELECT,INSERT,UPDATE ON testagram_local.posts TO service_role;
  END IF;
END;
$grants$;
COMMIT;
