-- Idempotent local post creation for the independent Testagram API.
BEGIN;

ALTER TABLE testagram_local.posts
  ADD COLUMN IF NOT EXISTS sensitive BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS testagram_local.post_idempotency (
  author_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  key_hash CHAR(64) NOT NULL CHECK (key_hash ~ '^[0-9a-f]{64}$'),
  request_hash CHAR(64) NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
  post_id UUID REFERENCES testagram_local.posts(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (author_id,key_hash)
);
CREATE INDEX IF NOT EXISTS testagram_local_post_idempotency_created_idx
  ON testagram_local.post_idempotency(created_at);

REVOKE ALL ON testagram_local.post_idempotency FROM PUBLIC;
DO $grants$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.post_idempotency TO testagram_app;
    GRANT SELECT,INSERT,UPDATE ON testagram_local.posts TO testagram_app;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
    GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.post_idempotency TO service_role;
    GRANT SELECT,INSERT,UPDATE ON testagram_local.posts TO service_role;
  END IF;
END;
$grants$;

COMMIT;
