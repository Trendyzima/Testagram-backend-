-- Native thread interaction records and counters.
BEGIN;
ALTER TABLE testagram_local.threads
  ADD COLUMN IF NOT EXISTS likes_count BIGINT NOT NULL DEFAULT 0 CHECK (likes_count >= 0),
  ADD COLUMN IF NOT EXISTS reposts_count BIGINT NOT NULL DEFAULT 0 CHECK (reposts_count >= 0),
  ADD COLUMN IF NOT EXISTS quotes_count BIGINT NOT NULL DEFAULT 0 CHECK (quotes_count >= 0),
  ADD COLUMN IF NOT EXISTS replies_count BIGINT NOT NULL DEFAULT 0 CHECK (replies_count >= 0),
  ADD COLUMN IF NOT EXISTS views_count BIGINT NOT NULL DEFAULT 0 CHECK (views_count >= 0);

CREATE TABLE IF NOT EXISTS testagram_local.thread_reposts (
  thread_id UUID NOT NULL REFERENCES testagram_local.threads(id) ON DELETE CASCADE,
  user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE RESTRICT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY(thread_id,user_id)
);
CREATE INDEX IF NOT EXISTS testagram_thread_reposts_user_created_idx
  ON testagram_local.thread_reposts(user_id,created_at DESC);

CREATE TABLE IF NOT EXISTS testagram_local.thread_likes (
  thread_id UUID NOT NULL REFERENCES testagram_local.threads(id) ON DELETE CASCADE,
  user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE RESTRICT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY(thread_id,user_id)
);
CREATE INDEX IF NOT EXISTS testagram_thread_likes_user_created_idx
  ON testagram_local.thread_likes(user_id,created_at DESC);
CREATE OR REPLACE FUNCTION testagram_local.update_thread_like_count()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='INSERT' THEN
    UPDATE testagram_local.threads SET likes_count=likes_count+1,updated_at=now() WHERE id=NEW.thread_id;
    RETURN NEW;
  ELSIF TG_OP='DELETE' THEN
    UPDATE testagram_local.threads SET likes_count=GREATEST(0,likes_count-1),updated_at=now() WHERE id=OLD.thread_id;
    RETURN OLD;
  END IF;
  RETURN NULL;
END;
$$;
DROP TRIGGER IF EXISTS testagram_thread_likes_count ON testagram_local.thread_likes;
CREATE TRIGGER testagram_thread_likes_count
AFTER INSERT OR DELETE ON testagram_local.thread_likes
FOR EACH ROW EXECUTE FUNCTION testagram_local.update_thread_like_count();

CREATE TABLE IF NOT EXISTS testagram_local.thread_bookmarks (
  thread_id UUID NOT NULL REFERENCES testagram_local.threads(id) ON DELETE CASCADE,
  user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE RESTRICT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY(thread_id,user_id)
);
CREATE INDEX IF NOT EXISTS testagram_thread_bookmarks_user_created_idx
  ON testagram_local.thread_bookmarks(user_id,created_at DESC);

CREATE OR REPLACE FUNCTION testagram_local.update_thread_repost_count()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='INSERT' THEN
    UPDATE testagram_local.threads SET reposts_count=reposts_count+1,updated_at=now() WHERE id=NEW.thread_id;
    RETURN NEW;
  ELSIF TG_OP='DELETE' THEN
    UPDATE testagram_local.threads SET reposts_count=GREATEST(0,reposts_count-1),updated_at=now() WHERE id=OLD.thread_id;
    RETURN OLD;
  END IF;
  RETURN NULL;
END;
$$;
DROP TRIGGER IF EXISTS testagram_thread_reposts_count ON testagram_local.thread_reposts;
CREATE TRIGGER testagram_thread_reposts_count
AFTER INSERT OR DELETE ON testagram_local.thread_reposts
FOR EACH ROW EXECUTE FUNCTION testagram_local.update_thread_repost_count();

ALTER TABLE testagram_local.thread_reposts ENABLE ROW LEVEL SECURITY;
ALTER TABLE testagram_local.thread_likes ENABLE ROW LEVEL SECURITY;
ALTER TABLE testagram_local.thread_bookmarks ENABLE ROW LEVEL SECURITY;
REVOKE ALL ON testagram_local.thread_reposts,testagram_local.thread_likes,testagram_local.thread_bookmarks FROM PUBLIC;
DO $acl$
BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
  EXECUTE 'GRANT SELECT,INSERT,DELETE ON testagram_local.thread_reposts TO service_role';
  EXECUTE 'GRANT SELECT,INSERT,DELETE ON testagram_local.thread_likes TO service_role';
  EXECUTE 'GRANT SELECT,INSERT,DELETE ON testagram_local.thread_bookmarks TO service_role';
 END IF;
END;
$acl$;
COMMENT ON TABLE testagram_local.thread_reposts IS 'Canonical per-account thread repost records; counters are maintained transactionally.';
COMMENT ON TABLE testagram_local.thread_likes IS 'Canonical per-account thread likes; counters are maintained transactionally.';
COMMENT ON TABLE testagram_local.thread_bookmarks IS 'Private per-account saved thread records.';
COMMIT;
