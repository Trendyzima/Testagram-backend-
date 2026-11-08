-- Canonical discovery metadata fields for accounts and posts.
BEGIN;
ALTER TABLE testagram_local.accounts
 ADD COLUMN IF NOT EXISTS verified BOOLEAN NOT NULL DEFAULT FALSE,
 ADD COLUMN IF NOT EXISTS verified_tier TEXT NOT NULL DEFAULT 'none' CHECK (length(verified_tier)<=32),
 ADD COLUMN IF NOT EXISTS is_creator BOOLEAN NOT NULL DEFAULT FALSE,
 ADD COLUMN IF NOT EXISTS creator_tier TEXT NOT NULL DEFAULT 'none' CHECK (length(creator_tier)<=32);
ALTER TABLE testagram_local.posts
 ADD COLUMN IF NOT EXISTS is_video BOOLEAN NOT NULL DEFAULT FALSE,
 ADD COLUMN IF NOT EXISTS views_count BIGINT NOT NULL DEFAULT 0 CHECK (views_count>=0);
CREATE INDEX IF NOT EXISTS testagram_local_posts_discovery_idx
 ON testagram_local.posts(created_at DESC,likes_count DESC,views_count DESC)
 WHERE deleted_at IS NULL AND visibility='public';
CREATE INDEX IF NOT EXISTS testagram_local_accounts_creator_idx
 ON testagram_local.accounts(follower_count DESC,username ASC)
 WHERE account_status='active' AND is_creator=TRUE AND discoverable_by_username AND NOT protected_account;
DO $grants$
BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
  GRANT SELECT,UPDATE ON testagram_local.accounts,testagram_local.posts TO testagram_app;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
  GRANT SELECT,UPDATE ON testagram_local.accounts,testagram_local.posts TO service_role;
 END IF;
END;
$grants$;
COMMIT;
