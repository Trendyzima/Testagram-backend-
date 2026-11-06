-- Bounded keyset home-feed access paths for the canonical PostgreSQL social graph.
-- Additive and safe to re-run.
BEGIN;
CREATE INDEX IF NOT EXISTS testagram_local_posts_public_feed_idx
  ON testagram_local.posts(created_at DESC,id DESC)
  WHERE deleted_at IS NULL AND visibility='public';
CREATE INDEX IF NOT EXISTS testagram_local_posts_author_feed_idx
  ON testagram_local.posts(author_id,created_at DESC,id DESC)
  WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS testagram_local_follows_feed_lookup_idx
  ON testagram_local.follows(follower_id,following_id)
  WHERE status='accepted';
CREATE INDEX IF NOT EXISTS testagram_local_mutes_feed_lookup_idx
  ON testagram_local.mutes(muter_id,muted_id);
CREATE INDEX IF NOT EXISTS testagram_local_blocks_feed_lookup_idx
  ON testagram_local.blocks(blocker_id,blocked_id);
COMMIT;
