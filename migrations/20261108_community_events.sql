-- Community event calendar and RSVP ownership on Neon.
BEGIN;
CREATE TABLE IF NOT EXISTS testagram_local.community_events (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  community_id UUID NOT NULL REFERENCES testagram_local.communities(id) ON DELETE CASCADE,
  creator_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE RESTRICT,
  title TEXT NOT NULL CHECK (length(btrim(title)) BETWEEN 3 AND 120),
  description TEXT NOT NULL DEFAULT '' CHECK (length(description)<=4000),
  starts_at TIMESTAMPTZ NOT NULL,
  ends_at TIMESTAMPTZ,
  location TEXT NOT NULL DEFAULT '' CHECK (length(location)<=240),
  event_url TEXT NOT NULL DEFAULT '' CHECK (length(event_url)<=2048),
  capacity INTEGER CHECK (capacity IS NULL OR capacity BETWEEN 1 AND 100000),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (ends_at IS NULL OR ends_at > starts_at)
);
CREATE INDEX IF NOT EXISTS testagram_community_events_calendar_idx
  ON testagram_local.community_events(community_id,starts_at,id);
CREATE TABLE IF NOT EXISTS testagram_local.community_event_rsvps (
  event_id UUID NOT NULL REFERENCES testagram_local.community_events(id) ON DELETE CASCADE,
  user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  status TEXT NOT NULL DEFAULT 'going' CHECK (status IN ('going','interested','cancelled')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY(event_id,user_id)
);
CREATE INDEX IF NOT EXISTS testagram_community_event_rsvps_user_idx
  ON testagram_local.community_event_rsvps(user_id,updated_at DESC);
REVOKE ALL ON testagram_local.community_events,testagram_local.community_event_rsvps FROM PUBLIC;
DO $grants$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    GRANT USAGE ON SCHEMA testagram_local TO testagram_app;
    GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.community_events,testagram_local.community_event_rsvps TO testagram_app;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
    GRANT USAGE ON SCHEMA testagram_local TO service_role;
    GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.community_events,testagram_local.community_event_rsvps TO service_role;
  END IF;
END;
$grants$;
COMMIT;
