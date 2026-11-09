-- Native post creation metadata and per-author idempotency.
-- Apply after 20261019_testagram_local_accounts_posts.sql.
BEGIN;

ALTER TABLE testagram_local.posts
  ADD COLUMN IF NOT EXISTS sensitive BOOLEAN NOT NULL DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS idempotency_key TEXT;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname = 'testagram_local_posts_idempotency_key_length'
      AND conrelid = 'testagram_local.posts'::regclass
  ) THEN
    ALTER TABLE testagram_local.posts
      ADD CONSTRAINT testagram_local_posts_idempotency_key_length
      CHECK (idempotency_key IS NULL OR length(idempotency_key) BETWEEN 1 AND 200);
  END IF;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS testagram_local_posts_author_idempotency
  ON testagram_local.posts(author_id,idempotency_key)
  WHERE idempotency_key IS NOT NULL;

REVOKE ALL ON testagram_local.posts FROM PUBLIC;

COMMIT;
