-- X-style quote threads and Bluesky-inspired user-selectable feed presets.
BEGIN;

CREATE TABLE IF NOT EXISTS testagram_local.thread_quotes (
  quote_thread_id uuid PRIMARY KEY REFERENCES testagram_local.threads(id) ON DELETE CASCADE,
  source_thread_id uuid NOT NULL REFERENCES testagram_local.threads(id) ON DELETE RESTRICT,
  owner_id uuid NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE RESTRICT,
  idempotency_key_hash text NOT NULL CHECK (idempotency_key_hash ~ '^[0-9a-f]{64}$'),
  request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
  created_at timestamptz NOT NULL DEFAULT now(),
  CHECK (quote_thread_id <> source_thread_id),
  UNIQUE(owner_id,idempotency_key_hash)
);
CREATE INDEX IF NOT EXISTS testagram_thread_quotes_source_created_idx
  ON testagram_local.thread_quotes(source_thread_id,created_at DESC);

CREATE OR REPLACE FUNCTION testagram_local.maintain_thread_quote_count()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF TG_OP='INSERT' THEN
    UPDATE testagram_local.threads
      SET quotes_count=quotes_count+1,updated_at=now()
      WHERE id=NEW.source_thread_id AND deleted_at IS NULL;
    RETURN NEW;
  ELSIF TG_OP='DELETE' THEN
    UPDATE testagram_local.threads
      SET quotes_count=greatest(0,quotes_count-1),updated_at=now()
      WHERE id=OLD.source_thread_id;
    RETURN OLD;
  END IF;
  RETURN NULL;
END;
$$;
DROP TRIGGER IF EXISTS testagram_thread_quotes_count_trigger ON testagram_local.thread_quotes;
CREATE TRIGGER testagram_thread_quotes_count_trigger
AFTER INSERT OR DELETE ON testagram_local.thread_quotes
FOR EACH ROW EXECUTE FUNCTION testagram_local.maintain_thread_quote_count();

CREATE TABLE IF NOT EXISTS testagram_local.user_feed_preferences (
  user_id uuid PRIMARY KEY REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  default_feed text NOT NULL DEFAULT 'for-you'
    CHECK (default_feed IN ('for-you','following','latest','media')),
  updated_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE testagram_local.thread_quotes ENABLE ROW LEVEL SECURITY;
ALTER TABLE testagram_local.user_feed_preferences ENABLE ROW LEVEL SECURITY;
REVOKE ALL ON testagram_local.thread_quotes,testagram_local.user_feed_preferences FROM PUBLIC;
DO $acl$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
    GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.thread_quotes TO service_role;
    GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.user_feed_preferences TO service_role;
  END IF;
END;
$acl$;

COMMENT ON TABLE testagram_local.thread_quotes IS 'Canonical quote-post relationship. Quote posts are ordinary threads with a source-thread reference and idempotent creation key.';
COMMENT ON TABLE testagram_local.user_feed_preferences IS 'Per-account default timeline preset; the user can switch between ranked, following, latest and media-focused views.';
COMMIT;
