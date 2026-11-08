-- Backend-owned hashtag challenge registry.
BEGIN;
CREATE TABLE IF NOT EXISTS testagram_local.hashtag_challenges (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 hashtag_id UUID NOT NULL REFERENCES testagram_local.hashtags(id) ON DELETE RESTRICT,
 created_by UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE RESTRICT,
 title TEXT NOT NULL CHECK(length(btrim(title)) BETWEEN 3 AND 120),
 description TEXT NOT NULL DEFAULT '' CHECK(length(description)<=2000),
 prize TEXT NOT NULL DEFAULT '' CHECK(length(prize)<=500),
 end_date TIMESTAMPTZ NOT NULL,
 is_active BOOLEAN NOT NULL DEFAULT TRUE,
 entry_count BIGINT NOT NULL DEFAULT 0 CHECK(entry_count>=0),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS testagram_hashtag_challenges_active_idx ON testagram_local.hashtag_challenges(is_active,end_date DESC,entry_count DESC);
CREATE INDEX IF NOT EXISTS testagram_hashtag_challenges_tag_idx ON testagram_local.hashtag_challenges(hashtag_id,end_date DESC);
DO $grants$
BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN GRANT SELECT,INSERT,UPDATE ON testagram_local.hashtag_challenges TO testagram_app; END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN GRANT SELECT,INSERT,UPDATE ON testagram_local.hashtag_challenges TO service_role; END IF;
END;
$grants$;
COMMIT;
