-- Backend-owned community events and RSVP state.
BEGIN;
CREATE TABLE IF NOT EXISTS testagram_local.community_events (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 community_id UUID NOT NULL REFERENCES testagram_local.communities(id) ON DELETE CASCADE,
 creator_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE RESTRICT,
 title TEXT NOT NULL CHECK(length(btrim(title)) BETWEEN 3 AND 160),
 description TEXT NOT NULL DEFAULT '' CHECK(length(description)<=4000),
 starts_at TIMESTAMPTZ NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS testagram_community_events_timeline_idx ON testagram_local.community_events(community_id,starts_at,id);
CREATE TABLE IF NOT EXISTS testagram_local.community_event_rsvps (
 event_id UUID NOT NULL REFERENCES testagram_local.community_events(id) ON DELETE CASCADE,
 user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(event_id,user_id)
);
CREATE INDEX IF NOT EXISTS testagram_community_event_rsvps_user_idx ON testagram_local.community_event_rsvps(user_id,created_at DESC);
DO $grants$
BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
  GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.community_events,testagram_local.community_event_rsvps TO testagram_app;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
  GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.community_events,testagram_local.community_event_rsvps TO service_role;
 END IF;
END;
$grants$;
COMMIT;
