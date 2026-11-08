BEGIN;
CREATE TABLE IF NOT EXISTS testagram_local.hashtags (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tag VARCHAR(64) NOT NULL UNIQUE CHECK (tag ~ '^[a-z0-9][a-z0-9_-]{1,63}$'),
  usage_count BIGINT NOT NULL DEFAULT 0 CHECK (usage_count >= 0),
  post_count BIGINT NOT NULL DEFAULT 0 CHECK (post_count >= 0),
  follower_count BIGINT NOT NULL DEFAULT 0 CHECK (follower_count >= 0),
  last_used_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS testagram_local.hashtag_follows (
  user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  hashtag_id UUID NOT NULL REFERENCES testagram_local.hashtags(id) ON DELETE CASCADE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY(user_id,hashtag_id)
);
CREATE INDEX IF NOT EXISTS hashtag_follows_hashtag_idx ON testagram_local.hashtag_follows(hashtag_id,created_at DESC);
CREATE INDEX IF NOT EXISTS hashtags_trending_idx ON testagram_local.hashtags(last_used_at DESC,usage_count DESC);
REVOKE ALL ON testagram_local.hashtags,testagram_local.hashtag_follows FROM PUBLIC;
DO $grants$
BEGIN
 IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
  GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.hashtags,testagram_local.hashtag_follows TO testagram_app;
 END IF;
 IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
  GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.hashtags,testagram_local.hashtag_follows TO service_role;
 END IF;
END $grants$;
COMMIT;
