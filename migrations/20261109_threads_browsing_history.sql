-- Canonical native storage for public conversation threads and private browsing history.
BEGIN;

CREATE TABLE IF NOT EXISTS testagram_local.threads (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  owner_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE RESTRICT,
  body TEXT NOT NULL DEFAULT '' CHECK (length(body) <= 500),
  visibility TEXT NOT NULL DEFAULT 'public' CHECK (visibility IN ('public','unlisted','private')),
  media_urls JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(media_urls)='array'),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at TIMESTAMPTZ NULL
);
CREATE INDEX IF NOT EXISTS testagram_threads_owner_created_idx
  ON testagram_local.threads(owner_id,created_at DESC) WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS testagram_local.browsing_history (
  user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  entity_type TEXT NOT NULL CHECK (entity_type IN ('post','thread','profile','video')),
  entity_id UUID NOT NULL,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata)='object'),
  viewed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY(user_id,entity_type,entity_id)
);
CREATE INDEX IF NOT EXISTS testagram_browsing_history_user_viewed_idx
  ON testagram_local.browsing_history(user_id,viewed_at DESC);

ALTER TABLE testagram_local.threads ENABLE ROW LEVEL SECURITY;
ALTER TABLE testagram_local.browsing_history ENABLE ROW LEVEL SECURITY;
REVOKE ALL ON testagram_local.threads,testagram_local.browsing_history FROM PUBLIC;
DO $acl$
BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
  EXECUTE 'GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.threads TO service_role';
  EXECUTE 'GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.browsing_history TO service_role';
 END IF;
END;
$acl$;
COMMENT ON TABLE testagram_local.threads IS 'Canonical API-owned thread records; attachments are validated against the native media registry.';
COMMENT ON TABLE testagram_local.browsing_history IS 'Private per-account browsing history; never exposed to other accounts.';
COMMIT;
