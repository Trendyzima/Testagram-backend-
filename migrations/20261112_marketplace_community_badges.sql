-- Backend-owned marketplace catalog and community achievement badges.
BEGIN;
CREATE TABLE IF NOT EXISTS testagram_local.marketplace_products (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 owner_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
 name TEXT NOT NULL CHECK(length(btrim(name)) BETWEEN 2 AND 160),
 description TEXT NOT NULL DEFAULT '' CHECK(length(description)<=4000),
 price NUMERIC(18,2) NOT NULL CHECK(price>=0),
 image_url TEXT NOT NULL DEFAULT '' CHECK(length(image_url)<=2048),
 external_link TEXT NOT NULL DEFAULT '' CHECK(length(external_link)<=2048),
 is_active BOOLEAN NOT NULL DEFAULT TRUE,
 is_featured BOOLEAN NOT NULL DEFAULT FALSE,
 sales_count BIGINT NOT NULL DEFAULT 0 CHECK(sales_count>=0),
 avg_rating NUMERIC(3,2) NOT NULL DEFAULT 0 CHECK(avg_rating BETWEEN 0 AND 5),
 review_count BIGINT NOT NULL DEFAULT 0 CHECK(review_count>=0),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS testagram_marketplace_active_featured_idx ON testagram_local.marketplace_products(is_featured,sales_count DESC,created_at DESC) WHERE is_active;
CREATE INDEX IF NOT EXISTS testagram_marketplace_active_name_idx ON testagram_local.marketplace_products(lower(name)) WHERE is_active;
CREATE TABLE IF NOT EXISTS testagram_local.community_badges (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 community_id UUID NOT NULL REFERENCES testagram_local.communities(id) ON DELETE CASCADE,
 owner_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
 badge_tier TEXT NOT NULL CHECK(badge_tier IN ('rare','epic','legendary')),
 contribution_score BIGINT NOT NULL DEFAULT 0 CHECK(contribution_score>=0),
 issued_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(community_id,owner_id)
);
CREATE INDEX IF NOT EXISTS testagram_community_badges_timeline_idx ON testagram_local.community_badges(community_id,issued_at DESC);
DO $grants$
BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
  GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.marketplace_products,testagram_local.community_badges TO testagram_app;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
  GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.marketplace_products,testagram_local.community_badges TO service_role;
 END IF;
END;
$grants$;
COMMIT;
