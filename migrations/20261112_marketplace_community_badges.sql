-- Backend-owned marketplace catalog; extend the pre-existing community achievement badge model.
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
ALTER TABLE testagram_local.marketplace_products
 ADD COLUMN IF NOT EXISTS category TEXT NOT NULL DEFAULT 'other' CHECK(length(category)<=48),
 ADD COLUMN IF NOT EXISTS views_count BIGINT NOT NULL DEFAULT 0 CHECK(views_count>=0);
CREATE INDEX IF NOT EXISTS testagram_marketplace_active_featured_idx ON testagram_local.marketplace_products(is_featured,sales_count DESC,created_at DESC) WHERE is_active;
CREATE INDEX IF NOT EXISTS testagram_marketplace_active_name_idx ON testagram_local.marketplace_products(lower(name)) WHERE is_active;
ALTER TABLE testagram_local.community_badges
 ADD COLUMN IF NOT EXISTS badge_tier TEXT NOT NULL DEFAULT 'rare',
 ADD COLUMN IF NOT EXISTS contribution_score BIGINT NOT NULL DEFAULT 0;
DO $constraints$
BEGIN
 IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='testagram_community_badges_tier_check') THEN
  ALTER TABLE testagram_local.community_badges ADD CONSTRAINT testagram_community_badges_tier_check CHECK(badge_tier IN ('rare','epic','legendary'));
 END IF;
 IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='testagram_community_badges_score_check') THEN
  ALTER TABLE testagram_local.community_badges ADD CONSTRAINT testagram_community_badges_score_check CHECK(contribution_score>=0);
 END IF;
END;
$constraints$;
UPDATE testagram_local.community_badges
 SET badge_tier=CASE badge_type WHEN 'founder' THEN 'legendary' WHEN 'contributor' THEN 'epic' ELSE 'rare' END
 WHERE contribution_score=0;
DO $grants$
BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
  GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.marketplace_products TO testagram_app;
  GRANT SELECT,INSERT,UPDATE ON testagram_local.community_badges TO testagram_app;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
  GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.marketplace_products TO service_role;
  GRANT SELECT,INSERT,UPDATE ON testagram_local.community_badges TO service_role;
 END IF;
END;
$grants$;
COMMIT;
