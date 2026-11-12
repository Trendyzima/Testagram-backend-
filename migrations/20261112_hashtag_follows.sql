-- User-owned hashtag interests. These follows are private and only influence
-- the authenticated viewer's Explore ranking; they never expose private posts.
BEGIN;

CREATE TABLE IF NOT EXISTS testagram_local.hashtag_follows (
    user_id UUID NOT NULL
        REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
    tag TEXT NOT NULL
        CHECK (char_length(tag) BETWEEN 1 AND 64 AND tag = lower(tag)),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, tag)
);

CREATE INDEX IF NOT EXISTS testagram_local_hashtag_follows_tag_idx
    ON testagram_local.hashtag_follows(tag, created_at DESC);

REVOKE ALL ON testagram_local.hashtag_follows FROM PUBLIC;

DO $grants$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    GRANT USAGE ON SCHEMA testagram_local TO testagram_app;
    GRANT SELECT, INSERT, DELETE ON testagram_local.hashtag_follows TO testagram_app;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
    GRANT USAGE ON SCHEMA testagram_local TO service_role;
    GRANT SELECT, INSERT, DELETE ON testagram_local.hashtag_follows TO service_role;
  END IF;
END;
$grants$;

COMMIT;
