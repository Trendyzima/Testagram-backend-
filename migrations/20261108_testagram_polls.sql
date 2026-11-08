-- Canonical poll storage for the independent Testagram social data plane.
-- API handlers own all access; browser clients never query these tables directly.
BEGIN;
CREATE TABLE IF NOT EXISTS testagram_local.polls (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 author_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE RESTRICT,
 post_id UUID NULL REFERENCES testagram_local.posts(id) ON DELETE SET NULL,
 question TEXT NOT NULL CHECK (length(btrim(question)) BETWEEN 5 AND 500),
 description TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 1000),
 status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','closed')),
 ends_at TIMESTAMPTZ NULL,
 media_object_key TEXT NULL REFERENCES testagram_media.assets(object_key) ON DELETE SET NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 CHECK (ends_at IS NULL OR ends_at > created_at)
);
CREATE INDEX IF NOT EXISTS testagram_local_polls_created_idx ON testagram_local.polls(created_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS testagram_local_polls_author_idx ON testagram_local.polls(author_id,created_at DESC);
CREATE TABLE IF NOT EXISTS testagram_local.poll_options (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 poll_id UUID NOT NULL REFERENCES testagram_local.polls(id) ON DELETE CASCADE,
 label TEXT NOT NULL CHECK (length(btrim(label)) BETWEEN 1 AND 200),
 position SMALLINT NOT NULL CHECK (position BETWEEN 0 AND 7),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(poll_id,position),
 UNIQUE(poll_id,id)
);
CREATE TABLE IF NOT EXISTS testagram_local.poll_votes (
 poll_id UUID NOT NULL,
 option_id UUID NOT NULL,
 user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE RESTRICT,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(poll_id,user_id),
 FOREIGN KEY(poll_id,option_id) REFERENCES testagram_local.poll_options(poll_id,id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS testagram_local_poll_votes_option_idx ON testagram_local.poll_votes(poll_id,option_id);
ALTER TABLE testagram_local.polls ENABLE ROW LEVEL SECURITY;
ALTER TABLE testagram_local.poll_options ENABLE ROW LEVEL SECURITY;
ALTER TABLE testagram_local.poll_votes ENABLE ROW LEVEL SECURITY;
REVOKE ALL ON testagram_local.polls,testagram_local.poll_options,testagram_local.poll_votes FROM PUBLIC;
DO $acl$
BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
  EXECUTE 'GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.polls TO service_role';
  EXECUTE 'GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.poll_options TO service_role';
  EXECUTE 'GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.poll_votes TO service_role';
 END IF;
END;
$acl$;
COMMENT ON TABLE testagram_local.polls IS 'Canonical poll records; API enforces visibility and owner-scoped writes.';
COMMENT ON TABLE testagram_local.poll_options IS 'Poll choices; API limits each poll to eight choices.';
COMMENT ON TABLE testagram_local.poll_votes IS 'One vote per account per poll; composite FK guarantees option belongs to poll.';
COMMIT;
