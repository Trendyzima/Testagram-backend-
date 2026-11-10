-- Independent PostgreSQL social graph and post-interaction foundation.
-- Additive only; legacy Supabase tables and user data are untouched.
BEGIN;

ALTER TABLE testagram_local.accounts
  ADD COLUMN IF NOT EXISTS follower_count BIGINT NOT NULL DEFAULT 0 CHECK (follower_count >= 0),
  ADD COLUMN IF NOT EXISTS following_count BIGINT NOT NULL DEFAULT 0 CHECK (following_count >= 0);
ALTER TABLE testagram_local.posts
  ADD COLUMN IF NOT EXISTS likes_count BIGINT NOT NULL DEFAULT 0 CHECK (likes_count >= 0),
  ADD COLUMN IF NOT EXISTS replies_count BIGINT NOT NULL DEFAULT 0 CHECK (replies_count >= 0),
  ADD COLUMN IF NOT EXISTS reposts_count BIGINT NOT NULL DEFAULT 0 CHECK (reposts_count >= 0);

CREATE TABLE IF NOT EXISTS testagram_local.follows (
  follower_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  following_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  status TEXT NOT NULL DEFAULT 'accepted' CHECK (status IN ('pending','accepted','rejected','blocked')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (follower_id, following_id),
  CHECK (follower_id <> following_id)
);

-- Upgrade any pre-existing local follows table in place. Earlier deployments
-- may have created only follower_id/following_id/status before this migration.
ALTER TABLE testagram_local.follows
  ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

DELETE FROM testagram_local.follows older
USING testagram_local.follows newer
WHERE older.ctid < newer.ctid
  AND older.follower_id = newer.follower_id
  AND older.following_id = newer.following_id;

CREATE UNIQUE INDEX IF NOT EXISTS testagram_local_follows_pair_uidx
  ON testagram_local.follows(follower_id, following_id);
CREATE INDEX IF NOT EXISTS testagram_local_follows_following_idx ON testagram_local.follows(following_id,created_at DESC,follower_id) WHERE status='accepted';
CREATE INDEX IF NOT EXISTS testagram_local_follows_follower_idx ON testagram_local.follows(follower_id,created_at DESC,following_id) WHERE status='accepted';
CREATE INDEX IF NOT EXISTS testagram_local_follows_pending_idx ON testagram_local.follows(following_id,created_at DESC) WHERE status='pending';

CREATE TABLE IF NOT EXISTS testagram_local.blocks (
  blocker_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  blocked_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (blocker_id,blocked_id),
  CHECK (blocker_id <> blocked_id)
);
CREATE INDEX IF NOT EXISTS testagram_local_blocks_blocked_idx ON testagram_local.blocks(blocked_id,blocker_id);

CREATE TABLE IF NOT EXISTS testagram_local.mutes (
  muter_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  muted_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (muter_id,muted_id),
  CHECK (muter_id <> muted_id)
);

CREATE TABLE IF NOT EXISTS testagram_local.post_reactions (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  post_id UUID NOT NULL REFERENCES testagram_local.posts(id) ON DELETE CASCADE,
  user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  emoji TEXT NOT NULL CHECK (length(emoji) BETWEEN 1 AND 32),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(post_id,user_id)
);
CREATE INDEX IF NOT EXISTS testagram_local_post_reactions_user_idx ON testagram_local.post_reactions(user_id,created_at DESC,post_id);
CREATE INDEX IF NOT EXISTS testagram_local_post_reactions_post_idx ON testagram_local.post_reactions(post_id,created_at DESC,user_id);

CREATE TABLE IF NOT EXISTS testagram_local.replies (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  post_id UUID NOT NULL REFERENCES testagram_local.posts(id) ON DELETE CASCADE,
  user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  parent_reply_id UUID REFERENCES testagram_local.replies(id) ON DELETE SET NULL,
  content TEXT NOT NULL CHECK (length(btrim(content)) BETWEEN 1 AND 10000),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS testagram_local_replies_post_created_idx ON testagram_local.replies(post_id,created_at DESC,id DESC) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS testagram_local_replies_user_created_idx ON testagram_local.replies(user_id,created_at DESC,id DESC) WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS testagram_local.reposts (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  post_id UUID NOT NULL REFERENCES testagram_local.posts(id) ON DELETE CASCADE,
  user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  quote TEXT NOT NULL DEFAULT '' CHECK (length(quote) <= 10000),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(post_id,user_id)
);
CREATE INDEX IF NOT EXISTS testagram_local_reposts_user_created_idx ON testagram_local.reposts(user_id,created_at DESC,post_id);
CREATE INDEX IF NOT EXISTS testagram_local_reposts_post_created_idx ON testagram_local.reposts(post_id,created_at DESC,user_id);

CREATE TABLE IF NOT EXISTS testagram_local.bookmarks (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  post_id UUID NOT NULL REFERENCES testagram_local.posts(id) ON DELETE CASCADE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(user_id,post_id)
);
CREATE INDEX IF NOT EXISTS testagram_local_bookmarks_user_created_idx ON testagram_local.bookmarks(user_id,created_at DESC,id DESC);

CREATE TABLE IF NOT EXISTS testagram_local.notifications (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  recipient_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  actor_id UUID REFERENCES testagram_local.accounts(user_id) ON DELETE SET NULL,
  post_id UUID REFERENCES testagram_local.posts(id) ON DELETE SET NULL,
  reply_id UUID REFERENCES testagram_local.replies(id) ON DELETE SET NULL,
  kind TEXT NOT NULL CHECK (length(kind) BETWEEN 1 AND 48),
  payload JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(payload)='object' AND octet_length(payload::text) <= 8192),
  event_key TEXT CHECK (event_key IS NULL OR length(event_key) BETWEEN 1 AND 256),
  read_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS testagram_local_notifications_event_key ON testagram_local.notifications(recipient_id,event_key) WHERE event_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS testagram_local_notifications_timeline_idx ON testagram_local.notifications(recipient_id,created_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS testagram_local_notifications_unread_idx ON testagram_local.notifications(recipient_id,created_at DESC,id DESC) WHERE read_at IS NULL;

-- Backfill counters for databases where social rows already exist before this migration.
UPDATE testagram_local.accounts a SET
  follower_count=(SELECT count(*) FROM testagram_local.follows f WHERE f.following_id=a.user_id AND f.status='accepted'),
  following_count=(SELECT count(*) FROM testagram_local.follows f WHERE f.follower_id=a.user_id AND f.status='accepted');
UPDATE testagram_local.posts p SET
  likes_count=(SELECT count(*) FROM testagram_local.post_reactions r WHERE r.post_id=p.id),
  replies_count=(SELECT count(*) FROM testagram_local.replies r WHERE r.post_id=p.id AND r.deleted_at IS NULL),
  reposts_count=(SELECT count(*) FROM testagram_local.reposts r WHERE r.post_id=p.id);

CREATE OR REPLACE FUNCTION testagram_local.adjust_follow_counts()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,testagram_local
AS $$
DECLARE old_accepted BOOLEAN := FALSE; new_accepted BOOLEAN := FALSE; follower UUID; following UUID;
BEGIN
  IF TG_OP <> 'INSERT' THEN old_accepted := OLD.status='accepted'; follower := OLD.follower_id; following := OLD.following_id; END IF;
  IF TG_OP <> 'DELETE' THEN new_accepted := NEW.status='accepted'; follower := NEW.follower_id; following := NEW.following_id; END IF;
  IF old_accepted AND NOT new_accepted THEN
    UPDATE testagram_local.accounts SET following_count=GREATEST(0,following_count-1) WHERE user_id=follower;
    UPDATE testagram_local.accounts SET follower_count=GREATEST(0,follower_count-1) WHERE user_id=following;
  ELSIF NOT old_accepted AND new_accepted THEN
    UPDATE testagram_local.accounts SET following_count=following_count+1 WHERE user_id=follower;
    UPDATE testagram_local.accounts SET follower_count=follower_count+1 WHERE user_id=following;
  END IF;
  IF TG_OP='DELETE' THEN RETURN OLD; END IF;
  RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION testagram_local.adjust_post_like_count()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,testagram_local
AS $$
BEGIN
  IF TG_OP='INSERT' THEN UPDATE testagram_local.posts SET likes_count=likes_count+1 WHERE id=NEW.post_id; RETURN NEW; END IF;
  UPDATE testagram_local.posts SET likes_count=GREATEST(0,likes_count-1) WHERE id=OLD.post_id;
  RETURN OLD;
END;
$$;

CREATE OR REPLACE FUNCTION testagram_local.adjust_post_reply_count()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,testagram_local
AS $$
DECLARE old_live BOOLEAN := FALSE; new_live BOOLEAN := FALSE; target_post UUID;
BEGIN
  IF TG_OP <> 'INSERT' THEN old_live := OLD.deleted_at IS NULL; target_post := OLD.post_id; END IF;
  IF TG_OP <> 'DELETE' THEN new_live := NEW.deleted_at IS NULL; target_post := NEW.post_id; END IF;
  IF old_live AND NOT new_live THEN UPDATE testagram_local.posts SET replies_count=GREATEST(0,replies_count-1) WHERE id=target_post;
  ELSIF NOT old_live AND new_live THEN UPDATE testagram_local.posts SET replies_count=replies_count+1 WHERE id=target_post; END IF;
  IF TG_OP='DELETE' THEN RETURN OLD; END IF;
  RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION testagram_local.adjust_post_repost_count()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,testagram_local
AS $$
BEGIN
  IF TG_OP='INSERT' THEN UPDATE testagram_local.posts SET reposts_count=reposts_count+1 WHERE id=NEW.post_id; RETURN NEW; END IF;
  UPDATE testagram_local.posts SET reposts_count=GREATEST(0,reposts_count-1) WHERE id=OLD.post_id;
  RETURN OLD;
END;
$$;

DROP TRIGGER IF EXISTS testagram_local_follows_count ON testagram_local.follows;
CREATE TRIGGER testagram_local_follows_count AFTER INSERT OR UPDATE OF status OR DELETE ON testagram_local.follows FOR EACH ROW EXECUTE FUNCTION testagram_local.adjust_follow_counts();
DROP TRIGGER IF EXISTS testagram_local_post_reactions_count ON testagram_local.post_reactions;
CREATE TRIGGER testagram_local_post_reactions_count AFTER INSERT OR DELETE ON testagram_local.post_reactions FOR EACH ROW EXECUTE FUNCTION testagram_local.adjust_post_like_count();
DROP TRIGGER IF EXISTS testagram_local_replies_count ON testagram_local.replies;
CREATE TRIGGER testagram_local_replies_count AFTER INSERT OR UPDATE OF deleted_at OR DELETE ON testagram_local.replies FOR EACH ROW EXECUTE FUNCTION testagram_local.adjust_post_reply_count();
DROP TRIGGER IF EXISTS testagram_local_reposts_count ON testagram_local.reposts;
CREATE TRIGGER testagram_local_reposts_count AFTER INSERT OR DELETE ON testagram_local.reposts FOR EACH ROW EXECUTE FUNCTION testagram_local.adjust_post_repost_count();

REVOKE ALL ON SCHEMA testagram_local FROM PUBLIC;
REVOKE ALL ON testagram_local.follows,testagram_local.blocks,testagram_local.mutes,testagram_local.post_reactions,
  testagram_local.replies,testagram_local.reposts,testagram_local.bookmarks,testagram_local.notifications FROM PUBLIC;
REVOKE ALL ON FUNCTION testagram_local.adjust_follow_counts() FROM PUBLIC;
REVOKE ALL ON FUNCTION testagram_local.adjust_post_like_count() FROM PUBLIC;
REVOKE ALL ON FUNCTION testagram_local.adjust_post_reply_count() FROM PUBLIC;
REVOKE ALL ON FUNCTION testagram_local.adjust_post_repost_count() FROM PUBLIC;

DO $grants$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    GRANT USAGE ON SCHEMA testagram_local TO testagram_app;
    GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.follows,testagram_local.blocks,testagram_local.mutes,
      testagram_local.post_reactions,testagram_local.replies,testagram_local.reposts,testagram_local.bookmarks,
      testagram_local.notifications TO testagram_app;
    GRANT SELECT,UPDATE ON testagram_local.accounts,testagram_local.posts TO testagram_app;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
    GRANT USAGE ON SCHEMA testagram_local TO service_role;
    GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.follows,testagram_local.blocks,testagram_local.mutes,
      testagram_local.post_reactions,testagram_local.replies,testagram_local.reposts,testagram_local.bookmarks,
      testagram_local.notifications TO service_role;
    GRANT SELECT,UPDATE ON testagram_local.accounts,testagram_local.posts TO service_role;
  END IF;
END;
$grants$;

COMMIT;
