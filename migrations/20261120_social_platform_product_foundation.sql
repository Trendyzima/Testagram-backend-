-- Testagram social-platform schema tranche.
-- Additive PostgreSQL structures for X-style lists/scheduling, ephemeral stories,
-- and auditable safety workflows. Canonical users remain testagram_auth identities;
-- product profiles/posts reference the existing testagram_local projection.
BEGIN;

-- Curated lists: ownership and membership are local product records. Authorization
-- is enforced by the canonical Backend before any service-role query.
CREATE TABLE IF NOT EXISTS testagram_local.social_lists (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  owner_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  name TEXT NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 64),
  description TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 280),
  visibility TEXT NOT NULL DEFAULT 'private' CHECK (visibility IN ('private','unlisted','public')),
  is_pinned BOOLEAN NOT NULL DEFAULT FALSE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS testagram_local_social_lists_owner_idx
  ON testagram_local.social_lists(owner_id, is_pinned DESC, updated_at DESC, id DESC)
  WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS testagram_local.social_list_members (
  list_id UUID NOT NULL REFERENCES testagram_local.social_lists(id) ON DELETE CASCADE,
  member_user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  added_by UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE RESTRICT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY(list_id, member_user_id)
);
CREATE INDEX IF NOT EXISTS testagram_local_social_list_members_user_idx
  ON testagram_local.social_list_members(member_user_id, created_at DESC, list_id);

CREATE TABLE IF NOT EXISTS testagram_local.social_list_posts (
  list_id UUID NOT NULL REFERENCES testagram_local.social_lists(id) ON DELETE CASCADE,
  post_id UUID NOT NULL REFERENCES testagram_local.posts(id) ON DELETE CASCADE,
  added_by UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE RESTRICT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY(list_id, post_id)
);
CREATE INDEX IF NOT EXISTS testagram_local_social_list_posts_timeline_idx
  ON testagram_local.social_list_posts(list_id, created_at DESC, post_id);

-- Drafts are private to their author. Scheduled rows become durable commands;
-- the worker must claim with SKIP LOCKED and publish through the same post
-- transaction/idempotency path as interactive publishing.
CREATE TABLE IF NOT EXISTS testagram_local.post_drafts (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  author_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  content TEXT NOT NULL DEFAULT '' CHECK (length(content) <= 10000),
  visibility TEXT NOT NULL DEFAULT 'public' CHECK (visibility IN ('public','followers','private')),
  media_keys JSONB NOT NULL DEFAULT '[]'::jsonb
    CHECK (jsonb_typeof(media_keys)='array' AND octet_length(media_keys::text) <= 16384),
  poll_payload JSONB NOT NULL DEFAULT '{}'::jsonb
    CHECK (jsonb_typeof(poll_payload)='object' AND octet_length(poll_payload::text) <= 8192),
  client_draft_id TEXT NOT NULL CHECK (length(client_draft_id) BETWEEN 8 AND 128),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(author_id, client_draft_id)
);
CREATE INDEX IF NOT EXISTS testagram_local_post_drafts_updated_idx
  ON testagram_local.post_drafts(author_id, updated_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS testagram_local.scheduled_posts (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  author_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  draft_id UUID REFERENCES testagram_local.post_drafts(id) ON DELETE SET NULL,
  content TEXT NOT NULL CHECK (length(btrim(content)) BETWEEN 1 AND 10000),
  visibility TEXT NOT NULL DEFAULT 'public' CHECK (visibility IN ('public','followers','private')),
  media_keys JSONB NOT NULL DEFAULT '[]'::jsonb
    CHECK (jsonb_typeof(media_keys)='array' AND octet_length(media_keys::text) <= 16384),
  scheduled_for TIMESTAMPTZ NOT NULL,
  status TEXT NOT NULL DEFAULT 'scheduled'
    CHECK (status IN ('scheduled','processing','published','cancelled','failed')),
  idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) BETWEEN 8 AND 128),
  published_post_id UUID REFERENCES testagram_local.posts(id) ON DELETE SET NULL,
  attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 20),
  locked_until TIMESTAMPTZ,
  last_error_code TEXT NOT NULL DEFAULT '' CHECK (length(last_error_code) <= 64),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(author_id, idempotency_key),
  CHECK ((status='published') = (published_post_id IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS testagram_local_scheduled_posts_claim_idx
  ON testagram_local.scheduled_posts(scheduled_for, created_at, id)
  WHERE status IN ('scheduled','processing');
CREATE INDEX IF NOT EXISTS testagram_local_scheduled_posts_author_idx
  ON testagram_local.scheduled_posts(author_id, scheduled_for DESC, id DESC);

-- Ephemeral stories reference object keys only. CDN/Data Centre still own bytes,
-- replication, authorization and expiry deletion; this schema is not a media store.
CREATE TABLE IF NOT EXISTS testagram_local.stories (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  author_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  media_key TEXT NOT NULL CHECK (length(media_key) BETWEEN 1 AND 512),
  media_type TEXT NOT NULL CHECK (media_type IN ('image','video','text')),
  caption TEXT NOT NULL DEFAULT '' CHECK (length(caption) <= 1000),
  visibility TEXT NOT NULL DEFAULT 'followers' CHECK (visibility IN ('public','followers','close_friends')),
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','expired','deleted','removed')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ NOT NULL,
  deleted_at TIMESTAMPTZ,
  CHECK (expires_at > created_at AND expires_at <= created_at + interval '7 days')
);
CREATE INDEX IF NOT EXISTS testagram_local_stories_author_idx
  ON testagram_local.stories(author_id, created_at DESC, id DESC)
  WHERE status='active' AND deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS testagram_local_stories_expiry_idx
  ON testagram_local.stories(expires_at, id)
  WHERE status='active' AND deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS testagram_local.story_views (
  story_id UUID NOT NULL REFERENCES testagram_local.stories(id) ON DELETE CASCADE,
  viewer_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  viewed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY(story_id, viewer_id)
);
CREATE INDEX IF NOT EXISTS testagram_local_story_views_viewer_idx
  ON testagram_local.story_views(viewer_id, viewed_at DESC);

CREATE TABLE IF NOT EXISTS testagram_local.story_reactions (
  story_id UUID NOT NULL REFERENCES testagram_local.stories(id) ON DELETE CASCADE,
  user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  reaction TEXT NOT NULL CHECK (length(reaction) BETWEEN 1 AND 32),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY(story_id, user_id)
);

-- Reports are append-only evidence. Moderation decisions are separate records
-- so appeals and reviewer history do not overwrite the original report.
CREATE TABLE IF NOT EXISTS testagram_local.social_reports (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  reporter_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE RESTRICT,
  target_type TEXT NOT NULL CHECK (target_type IN ('account','post','reply','thread','message','story')),
  target_id UUID NOT NULL,
  reason_code TEXT NOT NULL CHECK (reason_code IN
    ('spam','harassment','hate','violence','sexual_content','impersonation','privacy','copyright','self_harm','other')),
  details TEXT NOT NULL DEFAULT '' CHECK (length(details) <= 2000),
  status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','triaged','actioned','dismissed','appealed','closed')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(reporter_id, target_type, target_id)
);
CREATE INDEX IF NOT EXISTS testagram_local_social_reports_queue_idx
  ON testagram_local.social_reports(status, created_at, id)
  WHERE status IN ('open','triaged','appealed');

CREATE TABLE IF NOT EXISTS testagram_local.social_moderation_actions (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  report_id UUID REFERENCES testagram_local.social_reports(id) ON DELETE RESTRICT,
  target_type TEXT NOT NULL CHECK (target_type IN ('account','post','reply','thread','message','story')),
  target_id UUID NOT NULL,
  action TEXT NOT NULL CHECK (action IN ('no_action','label','limit_reach','remove_content','suspend_account','restore_content')),
  reason TEXT NOT NULL CHECK (length(btrim(reason)) BETWEEN 8 AND 2000),
  reviewer_user_id UUID NOT NULL REFERENCES testagram_auth.staff_principals(user_id) ON DELETE RESTRICT,
  expires_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS testagram_local_social_moderation_target_idx
  ON testagram_local.social_moderation_actions(target_type, target_id, created_at DESC);
CREATE INDEX IF NOT EXISTS testagram_local_social_moderation_reviewer_idx
  ON testagram_local.social_moderation_actions(reviewer_user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS testagram_local.social_moderation_appeals (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  action_id UUID NOT NULL REFERENCES testagram_local.social_moderation_actions(id) ON DELETE RESTRICT,
  appellant_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE RESTRICT,
  reason TEXT NOT NULL CHECK (length(btrim(reason)) BETWEEN 8 AND 2000),
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','upheld','reversed','withdrawn')),
  decided_by UUID REFERENCES testagram_auth.staff_principals(user_id) ON DELETE RESTRICT,
  decision_reason TEXT NOT NULL DEFAULT '' CHECK (length(decision_reason) <= 2000),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  decided_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS testagram_local_social_appeals_queue_idx
  ON testagram_local.social_moderation_appeals(status, created_at, id)
  WHERE status='pending';

-- All cross-organ product data is accessed through the canonical Backend.
-- No client, CDN, algorithm or federation database role receives direct access.
REVOKE ALL ON
  testagram_local.social_lists,
  testagram_local.social_list_members,
  testagram_local.social_list_posts,
  testagram_local.post_drafts,
  testagram_local.scheduled_posts,
  testagram_local.stories,
  testagram_local.story_views,
  testagram_local.story_reactions,
  testagram_local.social_reports,
  testagram_local.social_moderation_actions,
  testagram_local.social_moderation_appeals
FROM PUBLIC;

DO $grants$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    GRANT USAGE ON SCHEMA testagram_local TO testagram_app;
    GRANT USAGE ON SCHEMA testagram_auth TO testagram_app;
    GRANT SELECT,INSERT,UPDATE,DELETE ON
      testagram_local.social_lists,
      testagram_local.social_list_members,
      testagram_local.social_list_posts,
      testagram_local.post_drafts,
      testagram_local.scheduled_posts,
      testagram_local.stories,
      testagram_local.story_views,
      testagram_local.story_reactions,
      testagram_local.social_reports
    TO testagram_app;
    GRANT SELECT,INSERT ON testagram_local.social_moderation_actions TO testagram_app;
    GRANT SELECT,INSERT,UPDATE ON testagram_local.social_moderation_appeals TO testagram_app;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
    GRANT USAGE ON SCHEMA testagram_local TO service_role;
    GRANT USAGE ON SCHEMA testagram_auth TO service_role;
    GRANT SELECT,INSERT,UPDATE,DELETE ON
      testagram_local.social_lists,
      testagram_local.social_list_members,
      testagram_local.social_list_posts,
      testagram_local.post_drafts,
      testagram_local.scheduled_posts,
      testagram_local.stories,
      testagram_local.story_views,
      testagram_local.story_reactions,
      testagram_local.social_reports
    TO service_role;
    GRANT SELECT,INSERT ON testagram_local.social_moderation_actions TO service_role;
    GRANT SELECT,INSERT,UPDATE ON testagram_local.social_moderation_appeals TO service_role;
  END IF;
END;
$grants$;

COMMENT ON TABLE testagram_local.social_lists IS 'Curated local-account timelines; authorization is enforced by the canonical Backend.';
COMMENT ON TABLE testagram_local.post_drafts IS 'Private per-author drafts; never included in public replication or federation.';
COMMENT ON TABLE testagram_local.scheduled_posts IS 'Durable scheduled publish commands; worker must reuse canonical post idempotency.';
COMMENT ON TABLE testagram_local.stories IS 'Ephemeral story metadata; media bytes remain in the Data Centre/CDN lifecycle.';
COMMENT ON TABLE testagram_local.social_reports IS 'User-submitted safety reports with immutable original evidence.';
COMMENT ON TABLE testagram_local.social_moderation_actions IS 'Staff moderation decisions linked to native Testagram staff principals.';

COMMIT;
