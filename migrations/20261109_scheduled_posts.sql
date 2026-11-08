-- Durable scheduled community posts and idempotent user notifications.
BEGIN;
CREATE TABLE IF NOT EXISTS testagram_local.scheduled_posts (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  community_id UUID REFERENCES testagram_local.communities(id) ON DELETE SET NULL,
  content TEXT NOT NULL CHECK (length(content) BETWEEN 1 AND 10000),
  scheduled_for TIMESTAMPTZ NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','publishing','published','failed','cancelled')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  published_post_id UUID REFERENCES testagram_local.posts(id) ON DELETE SET NULL,
  CHECK (scheduled_for > created_at - interval '1 minute')
);
CREATE INDEX IF NOT EXISTS testagram_local_scheduled_posts_due_idx
  ON testagram_local.scheduled_posts(scheduled_for,id) WHERE status='pending';
CREATE INDEX IF NOT EXISTS testagram_local_scheduled_posts_user_idx
  ON testagram_local.scheduled_posts(user_id,scheduled_for DESC,id DESC);
DO $grants$
BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
   GRANT SELECT,INSERT,UPDATE ON testagram_local.scheduled_posts TO testagram_app;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
   GRANT SELECT,INSERT,UPDATE ON testagram_local.scheduled_posts TO service_role;
 END IF;
END;
$grants$;
COMMIT;
