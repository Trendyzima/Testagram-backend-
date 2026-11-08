-- Canonical Testagram community foundation on the independent PostgreSQL data plane.
-- Community identity, membership and discovery are owned by the Backend, not a legacy RPC.
BEGIN;
CREATE TABLE IF NOT EXISTS testagram_local.communities (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  owner_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE RESTRICT,
  slug TEXT NOT NULL CHECK (slug ~ '^[a-z0-9][a-z0-9_-]{2,39}$'),
  name TEXT NOT NULL CHECK (length(btrim(name)) BETWEEN 3 AND 80),
  description TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 2000),
  visibility TEXT NOT NULL DEFAULT 'public' CHECK (visibility IN ('public','private')),
  icon_url TEXT NOT NULL DEFAULT '' CHECK (length(icon_url) <= 2048),
  banner_url TEXT NOT NULL DEFAULT '' CHECK (length(banner_url) <= 2048),
  rules JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(rules)='array' AND octet_length(rules::text)<=8192),
  member_count BIGINT NOT NULL DEFAULT 0 CHECK (member_count >= 0),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (slug)
);
CREATE INDEX IF NOT EXISTS testagram_communities_discovery_idx
  ON testagram_local.communities(member_count DESC, created_at DESC, id DESC)
  WHERE visibility='public';
CREATE TABLE IF NOT EXISTS testagram_local.community_memberships (
  community_id UUID NOT NULL REFERENCES testagram_local.communities(id) ON DELETE CASCADE,
  user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  role TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('owner','admin','moderator','member')),
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','pending','banned')),
  joined_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (community_id,user_id)
);
CREATE INDEX IF NOT EXISTS testagram_community_members_user_idx
  ON testagram_local.community_memberships(user_id,joined_at DESC,community_id);
CREATE INDEX IF NOT EXISTS testagram_community_members_active_idx
  ON testagram_local.community_memberships(community_id,joined_at DESC,user_id)
  WHERE status='active';
CREATE OR REPLACE FUNCTION testagram_local.adjust_community_member_count()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,testagram_local
AS $$
DECLARE old_active BOOLEAN := FALSE; new_active BOOLEAN := FALSE; cid UUID;
BEGIN
  IF TG_OP <> 'INSERT' THEN old_active := OLD.status='active'; cid := OLD.community_id; END IF;
  IF TG_OP <> 'DELETE' THEN new_active := NEW.status='active'; cid := NEW.community_id; END IF;
  IF old_active AND NOT new_active THEN
    UPDATE testagram_local.communities SET member_count=GREATEST(0,member_count-1),updated_at=now() WHERE id=cid;
  ELSIF NOT old_active AND new_active THEN
    UPDATE testagram_local.communities SET member_count=member_count+1,updated_at=now() WHERE id=cid;
  END IF;
  IF TG_OP='DELETE' THEN RETURN OLD; END IF;
  RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS testagram_community_membership_count ON testagram_local.community_memberships;
CREATE TRIGGER testagram_community_membership_count
AFTER INSERT OR UPDATE OF status OR DELETE ON testagram_local.community_memberships
FOR EACH ROW EXECUTE FUNCTION testagram_local.adjust_community_member_count();
REVOKE ALL ON testagram_local.communities,testagram_local.community_memberships FROM PUBLIC;
REVOKE ALL ON FUNCTION testagram_local.adjust_community_member_count() FROM PUBLIC;
DO $grants$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    GRANT USAGE ON SCHEMA testagram_local TO testagram_app;
    GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.communities,testagram_local.community_memberships TO testagram_app;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
    GRANT USAGE ON SCHEMA testagram_local TO service_role;
    GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.communities,testagram_local.community_memberships TO service_role;
  END IF;
END;
$grants$;
COMMIT;
