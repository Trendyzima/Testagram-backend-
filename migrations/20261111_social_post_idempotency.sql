-- Durable idempotency for native Testagram post creation.
-- A client retry cannot create a duplicate post or reuse a key for new content.
BEGIN;

CREATE TABLE IF NOT EXISTS testagram_local.post_create_idempotency (
  author_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  key_hash TEXT NOT NULL CHECK (key_hash ~ '^[a-f0-9]{64}$'),
  request_hash TEXT NOT NULL CHECK (request_hash ~ '^[a-f0-9]{64}$'),
  post_id UUID NOT NULL UNIQUE REFERENCES testagram_local.posts(id) ON DELETE CASCADE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (author_id, key_hash)
);

CREATE INDEX IF NOT EXISTS testagram_local_post_create_idempotency_created_idx
  ON testagram_local.post_create_idempotency(created_at);

REVOKE ALL ON testagram_local.post_create_idempotency FROM PUBLIC;
DO $grants$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
    GRANT SELECT, INSERT ON testagram_local.post_create_idempotency TO service_role;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    GRANT SELECT, INSERT ON testagram_local.post_create_idempotency TO testagram_app;
  END IF;
END;
$grants$;

COMMIT;
