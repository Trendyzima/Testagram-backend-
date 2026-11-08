-- Native Backend polls and votes for posts.
BEGIN;
CREATE TABLE IF NOT EXISTS testagram_local.polls (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  post_id UUID NOT NULL UNIQUE REFERENCES testagram_local.posts(id) ON DELETE CASCADE,
  question TEXT NOT NULL CHECK (length(btrim(question)) BETWEEN 1 AND 500),
  description TEXT,
  status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','closed')),
  ends_at TIMESTAMPTZ,
  total_votes BIGINT NOT NULL DEFAULT 0 CHECK (total_votes >= 0),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS testagram_local.poll_options (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  poll_id UUID NOT NULL REFERENCES testagram_local.polls(id) ON DELETE CASCADE,
  label TEXT NOT NULL CHECK (length(btrim(label)) BETWEEN 1 AND 160),
  position INTEGER NOT NULL CHECK (position BETWEEN 0 AND 9),
  votes BIGINT NOT NULL DEFAULT 0 CHECK (votes >= 0),
  UNIQUE(poll_id,position),
  UNIQUE(poll_id,id)
);
CREATE TABLE IF NOT EXISTS testagram_local.poll_votes (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  poll_id UUID NOT NULL REFERENCES testagram_local.polls(id) ON DELETE CASCADE,
  option_id UUID NOT NULL,
  voter_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(poll_id,voter_id),
  FOREIGN KEY(poll_id,option_id) REFERENCES testagram_local.poll_options(poll_id,id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS testagram_local_poll_votes_option_idx ON testagram_local.poll_votes(poll_id,option_id);
DO $grants$
BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
  GRANT SELECT,INSERT,UPDATE ON testagram_local.polls,testagram_local.poll_options,testagram_local.poll_votes TO testagram_app;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
  GRANT SELECT,INSERT,UPDATE ON testagram_local.polls,testagram_local.poll_options,testagram_local.poll_votes TO service_role;
 END IF;
END;
$grants$;
COMMIT;
