-- Backend-owned local hashtags, post indexing, and follow graph.
BEGIN;
CREATE TABLE IF NOT EXISTS testagram_local.hashtags (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 tag TEXT NOT NULL UNIQUE CHECK (tag ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
 usage_count BIGINT NOT NULL DEFAULT 0 CHECK (usage_count>=0),
 follower_count BIGINT NOT NULL DEFAULT 0 CHECK (follower_count>=0),
 federated_post_count BIGINT NOT NULL DEFAULT 0 CHECK (federated_post_count>=0),
 last_used_at TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS testagram_local.post_hashtags (
 post_id UUID NOT NULL REFERENCES testagram_local.posts(id) ON DELETE CASCADE,
 hashtag_id UUID NOT NULL REFERENCES testagram_local.hashtags(id) ON DELETE CASCADE,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(post_id,hashtag_id)
);
CREATE INDEX IF NOT EXISTS testagram_local_post_hashtags_tag_idx ON testagram_local.post_hashtags(hashtag_id,created_at DESC,post_id);
CREATE TABLE IF NOT EXISTS testagram_local.hashtag_follows (
 user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
 hashtag_id UUID NOT NULL REFERENCES testagram_local.hashtags(id) ON DELETE CASCADE,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(user_id,hashtag_id)
);
CREATE INDEX IF NOT EXISTS testagram_local_hashtag_follows_tag_idx ON testagram_local.hashtag_follows(hashtag_id,created_at DESC);
CREATE OR REPLACE FUNCTION testagram_local.adjust_hashtag_post_count()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,testagram_local
AS $$
BEGIN
 IF TG_OP='INSERT' THEN
  UPDATE testagram_local.hashtags SET usage_count=usage_count+1,last_used_at=now(),updated_at=now() WHERE id=NEW.hashtag_id;
  RETURN NEW;
 END IF;
 UPDATE testagram_local.hashtags SET usage_count=GREATEST(0,usage_count-1),updated_at=now() WHERE id=OLD.hashtag_id;
 RETURN OLD;
END;
$$;
CREATE OR REPLACE FUNCTION testagram_local.adjust_hashtag_follower_count()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,testagram_local
AS $$
BEGIN
 IF TG_OP='INSERT' THEN UPDATE testagram_local.hashtags SET follower_count=follower_count+1,updated_at=now() WHERE id=NEW.hashtag_id; RETURN NEW; END IF;
 UPDATE testagram_local.hashtags SET follower_count=GREATEST(0,follower_count-1),updated_at=now() WHERE id=OLD.hashtag_id; RETURN OLD;
END;
$$;
DROP TRIGGER IF EXISTS testagram_local_post_hashtag_count ON testagram_local.post_hashtags;
CREATE TRIGGER testagram_local_post_hashtag_count AFTER INSERT OR DELETE ON testagram_local.post_hashtags FOR EACH ROW EXECUTE FUNCTION testagram_local.adjust_hashtag_post_count();
DROP TRIGGER IF EXISTS testagram_local_hashtag_follow_count ON testagram_local.hashtag_follows;
CREATE TRIGGER testagram_local_hashtag_follow_count AFTER INSERT OR DELETE ON testagram_local.hashtag_follows FOR EACH ROW EXECUTE FUNCTION testagram_local.adjust_hashtag_follower_count();
CREATE OR REPLACE FUNCTION testagram_local.index_post_hashtags()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,testagram_local
AS $
DECLARE match_row TEXT[];
BEGIN
 DELETE FROM testagram_local.post_hashtags WHERE post_id=NEW.id;
 IF NEW.deleted_at IS NOT NULL THEN RETURN NEW; END IF;
 FOR match_row IN SELECT regexp_matches(lower(COALESCE(NEW.text,'')),'#([a-z0-9_]{1,64})','g') LOOP
  INSERT INTO testagram_local.hashtags(tag) VALUES(match_row[1]) ON CONFLICT(tag) DO NOTHING;
  INSERT INTO testagram_local.post_hashtags(post_id,hashtag_id)
   SELECT NEW.id,id FROM testagram_local.hashtags WHERE tag=match_row[1]
   ON CONFLICT(post_id,hashtag_id) DO NOTHING;
 END LOOP;
 RETURN NEW;
END;
$;
DROP TRIGGER IF EXISTS testagram_local_posts_hashtag_index ON testagram_local.posts;
CREATE TRIGGER testagram_local_posts_hashtag_index AFTER INSERT OR UPDATE OF text,deleted_at ON testagram_local.posts
FOR EACH ROW EXECUTE FUNCTION testagram_local.index_post_hashtags();

-- Backfill existing visible posts so the first API read is complete.
WITH extracted AS (
 SELECT p.id AS post_id, lower(matches[1]) AS tag
 FROM testagram_local.posts p
 CROSS JOIN LATERAL regexp_matches(lower(COALESCE(p.text,'')),'#([a-z0-9_]{1,64})','g') AS matches
 WHERE p.deleted_at IS NULL
), inserted AS (
 INSERT INTO testagram_local.hashtags(tag)
 SELECT DISTINCT tag FROM extracted ON CONFLICT(tag) DO NOTHING RETURNING id,tag
)
INSERT INTO testagram_local.post_hashtags(post_id,hashtag_id)
SELECT e.post_id,h.id FROM extracted e JOIN testagram_local.hashtags h ON h.tag=e.tag
ON CONFLICT(post_id,hashtag_id) DO NOTHING;
DO $grants$
BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
  GRANT SELECT,INSERT,DELETE ON testagram_local.hashtags,testagram_local.post_hashtags,testagram_local.hashtag_follows TO testagram_app;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
  GRANT SELECT,INSERT,DELETE ON testagram_local.hashtags,testagram_local.post_hashtags,testagram_local.hashtag_follows TO service_role;
 END IF;
END;
$grants$;
COMMIT;
