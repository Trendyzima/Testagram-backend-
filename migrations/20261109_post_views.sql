-- Native view counter for API-owned social posts.
BEGIN;
ALTER TABLE testagram_local.posts
  ADD COLUMN IF NOT EXISTS views_count BIGINT NOT NULL DEFAULT 0 CHECK (views_count >= 0);
CREATE INDEX IF NOT EXISTS testagram_local_posts_views_created_idx
  ON testagram_local.posts(views_count DESC,created_at DESC) WHERE deleted_at IS NULL AND visibility='public';
COMMIT;
