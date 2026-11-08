-- Backend-owned ephemeral story metadata and idempotent view tracking.
BEGIN;
CREATE TABLE IF NOT EXISTS testagram_local.stories (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
 media_url TEXT NOT NULL CHECK(length(media_url) BETWEEN 1 AND 2048),
 media_type TEXT NOT NULL CHECK(media_type IN ('image','video')),
 caption TEXT NOT NULL DEFAULT '' CHECK(length(caption)<=1000),
 views_count BIGINT NOT NULL DEFAULT 0 CHECK(views_count>=0),
 expires_at TIMESTAMPTZ NOT NULL DEFAULT (now()+interval '24 hours'),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 deleted_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS testagram_stories_explore_idx ON testagram_local.stories(views_count DESC,created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS testagram_stories_expiry_idx ON testagram_local.stories(expires_at) WHERE deleted_at IS NULL;
CREATE TABLE IF NOT EXISTS testagram_local.story_views (
 story_id UUID NOT NULL REFERENCES testagram_local.stories(id) ON DELETE CASCADE,
 viewer_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
 viewed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(story_id,viewer_id)
);
DO $grants$
BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.stories,testagram_local.story_views TO testagram_app; END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.stories,testagram_local.story_views TO service_role; END IF;
END;
$grants$;
COMMIT;
