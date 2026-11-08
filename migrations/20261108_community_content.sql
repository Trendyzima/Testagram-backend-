-- Community content and profile extensions for the canonical Neon data plane.
BEGIN;
ALTER TABLE testagram_local.communities
  ADD COLUMN IF NOT EXISTS category TEXT NOT NULL DEFAULT 'general' CHECK (length(category) BETWEEN 1 AND 48),
  ADD COLUMN IF NOT EXISTS post_count BIGINT NOT NULL DEFAULT 0 CHECK (post_count >= 0),
  ADD COLUMN IF NOT EXISTS pinned_post_ids JSONB NOT NULL DEFAULT '[]'::jsonb
    CHECK (jsonb_typeof(pinned_post_ids)='array' AND octet_length(pinned_post_ids::text)<=8192);
ALTER TABLE testagram_local.posts
  ADD COLUMN IF NOT EXISTS community_id UUID REFERENCES testagram_local.communities(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS testagram_local_community_posts_idx
  ON testagram_local.posts(community_id,created_at DESC,id DESC)
  WHERE community_id IS NOT NULL AND deleted_at IS NULL;
CREATE OR REPLACE FUNCTION testagram_local.adjust_community_post_count()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,testagram_local
AS $$
DECLARE old_live BOOLEAN := FALSE; new_live BOOLEAN := FALSE; old_community UUID; new_community UUID;
BEGIN
  IF TG_OP <> 'INSERT' THEN
    old_live := OLD.deleted_at IS NULL AND OLD.community_id IS NOT NULL;
    old_community := OLD.community_id;
  END IF;
  IF TG_OP <> 'DELETE' THEN
    new_live := NEW.deleted_at IS NULL AND NEW.community_id IS NOT NULL;
    new_community := NEW.community_id;
  END IF;
  IF old_live AND (NOT new_live OR old_community IS DISTINCT FROM new_community) THEN
    UPDATE testagram_local.communities SET post_count=GREATEST(0,post_count-1),updated_at=now() WHERE id=old_community;
  END IF;
  IF new_live AND (NOT old_live OR old_community IS DISTINCT FROM new_community) THEN
    UPDATE testagram_local.communities SET post_count=post_count+1,updated_at=now() WHERE id=new_community;
  END IF;
  IF TG_OP='DELETE' THEN RETURN OLD; END IF;
  RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS testagram_local_community_post_count ON testagram_local.posts;
CREATE TRIGGER testagram_local_community_post_count
AFTER INSERT OR UPDATE OF community_id,deleted_at OR DELETE ON testagram_local.posts
FOR EACH ROW EXECUTE FUNCTION testagram_local.adjust_community_post_count();
UPDATE testagram_local.communities c
SET post_count=(SELECT count(*) FROM testagram_local.posts p WHERE p.community_id=c.id AND p.deleted_at IS NULL);
REVOKE ALL ON FUNCTION testagram_local.adjust_community_post_count() FROM PUBLIC;
COMMIT;
