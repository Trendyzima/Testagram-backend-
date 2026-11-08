-- Backend-owned community achievement badges (application-issued, not chain tokens).
BEGIN;
CREATE TABLE IF NOT EXISTS testagram_local.community_badges (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  community_id UUID NOT NULL REFERENCES testagram_local.communities(id) ON DELETE CASCADE,
  owner_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  badge_type TEXT NOT NULL DEFAULT 'member' CHECK (badge_type IN ('member','founder','contributor')),
  minted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (community_id,owner_id)
);
CREATE INDEX IF NOT EXISTS testagram_local_community_badges_timeline_idx
  ON testagram_local.community_badges(community_id,minted_at DESC,id DESC);
DO $grants$
BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
   GRANT SELECT,INSERT ON testagram_local.community_badges TO testagram_app;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
   GRANT SELECT,INSERT ON testagram_local.community_badges TO service_role;
 END IF;
END;
$grants$;
COMMIT;
