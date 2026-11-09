-- Authenticated, deduplicated post views for the canonical social API.
BEGIN;

CREATE TABLE IF NOT EXISTS testagram_local.post_views (
  post_id UUID NOT NULL REFERENCES testagram_local.posts(id) ON DELETE CASCADE,
  viewer_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  view_date DATE NOT NULL DEFAULT (now() AT TIME ZONE 'UTC')::date,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (post_id, viewer_id, view_date)
);

CREATE INDEX IF NOT EXISTS testagram_local_post_views_viewer_idx
  ON testagram_local.post_views(viewer_id, created_at DESC);

REVOKE ALL ON testagram_local.post_views FROM PUBLIC;
DO $grants$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    GRANT SELECT, INSERT ON testagram_local.post_views TO testagram_app;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
    GRANT SELECT, INSERT ON testagram_local.post_views TO service_role;
  END IF;
END;
$grants$;

COMMIT;
