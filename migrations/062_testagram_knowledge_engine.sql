-- Testagram first-class knowledge engine.
-- Additive schema only: canonical local identities, authorization and moderation
-- remain owned by the Backend. Q&A answers are not social post replies.
BEGIN;

CREATE TABLE IF NOT EXISTS testagram_local.topics (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  slug TEXT NOT NULL CHECK (slug ~ '^[a-z0-9][a-z0-9-]{1,62}$'),
  name TEXT NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 80),
  description TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 500),
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','retired')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (slug)
);
CREATE INDEX IF NOT EXISTS testagram_local_topics_active_name_idx
  ON testagram_local.topics(lower(name), id) WHERE status='active';

CREATE TABLE IF NOT EXISTS testagram_local.questions (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  author_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE RESTRICT,
  title TEXT NOT NULL CHECK (length(btrim(title)) BETWEEN 1 AND 180),
  body TEXT NOT NULL DEFAULT '' CHECK (length(body) <= 20000),
  visibility TEXT NOT NULL DEFAULT 'public' CHECK (visibility IN ('public','followers','private')),
  status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','answered','closed','removed')),
  accepted_answer_id UUID,
  language TEXT NOT NULL DEFAULT '' CHECK (length(language) <= 32),
  version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at TIMESTAMPTZ,
  CHECK (status <> 'answered' OR accepted_answer_id IS NOT NULL),
  CHECK (accepted_answer_id IS NULL OR status IN ('answered','closed')),
  CHECK (status <> 'removed' OR accepted_answer_id IS NULL)
);
CREATE INDEX IF NOT EXISTS testagram_local_questions_author_created_idx
  ON testagram_local.questions(author_id, created_at DESC, id DESC)
  WHERE deleted_at IS NULL AND status <> 'removed';
CREATE INDEX IF NOT EXISTS testagram_local_questions_public_created_idx
  ON testagram_local.questions(created_at DESC, id DESC)
  WHERE deleted_at IS NULL AND status IN ('open','answered','closed') AND visibility='public';
CREATE INDEX IF NOT EXISTS testagram_local_questions_open_created_idx
  ON testagram_local.questions(created_at DESC, id DESC)
  WHERE deleted_at IS NULL AND status='open';

CREATE TABLE IF NOT EXISTS testagram_local.answers (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  question_id UUID NOT NULL REFERENCES testagram_local.questions(id) ON DELETE RESTRICT,
  author_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE RESTRICT,
  body TEXT NOT NULL CHECK (length(btrim(body)) BETWEEN 1 AND 20000),
  revision_number INTEGER NOT NULL DEFAULT 1 CHECK (revision_number > 0),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at TIMESTAMPTZ,
  UNIQUE (question_id, id)
);
CREATE INDEX IF NOT EXISTS testagram_local_answers_question_created_idx
  ON testagram_local.answers(question_id, created_at DESC, id DESC)
  WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS testagram_local_answers_author_created_idx
  ON testagram_local.answers(author_id, created_at DESC, id DESC)
  WHERE deleted_at IS NULL;

-- Composite FK prevents a question from pointing at an answer belonging to a
-- different question. App handlers must clear acceptance before soft deletion.
DO $constraint$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid='testagram_local.questions'::regclass
      AND conname='testagram_questions_accepted_answer_same_question_fk'
  ) THEN
    ALTER TABLE testagram_local.questions
      ADD CONSTRAINT testagram_questions_accepted_answer_same_question_fk
      FOREIGN KEY (id, accepted_answer_id)
      REFERENCES testagram_local.answers(question_id, id)
      ON DELETE RESTRICT DEFERRABLE INITIALLY IMMEDIATE;
  END IF;
END
$constraint$;

CREATE TABLE IF NOT EXISTS testagram_local.answer_revisions (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  answer_id UUID NOT NULL REFERENCES testagram_local.answers(id) ON DELETE RESTRICT,
  editor_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE RESTRICT,
  revision_number INTEGER NOT NULL CHECK (revision_number > 0),
  previous_body TEXT NOT NULL CHECK (length(previous_body) <= 20000),
  body TEXT NOT NULL CHECK (length(btrim(body)) BETWEEN 1 AND 20000),
  edit_reason TEXT NOT NULL DEFAULT '' CHECK (length(edit_reason) <= 500),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(answer_id, revision_number)
);
CREATE INDEX IF NOT EXISTS testagram_local_answer_revisions_history_idx
  ON testagram_local.answer_revisions(answer_id, revision_number DESC);

CREATE TABLE IF NOT EXISTS testagram_local.question_topics (
  question_id UUID NOT NULL REFERENCES testagram_local.questions(id) ON DELETE RESTRICT,
  topic_id UUID NOT NULL REFERENCES testagram_local.topics(id) ON DELETE RESTRICT,
  added_by UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE RESTRICT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY(question_id, topic_id)
);
CREATE INDEX IF NOT EXISTS testagram_local_question_topics_topic_idx
  ON testagram_local.question_topics(topic_id, question_id);

CREATE TABLE IF NOT EXISTS testagram_local.topic_follows (
  user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE RESTRICT,
  topic_id UUID NOT NULL REFERENCES testagram_local.topics(id) ON DELETE RESTRICT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY(user_id, topic_id)
);
CREATE INDEX IF NOT EXISTS testagram_local_topic_follows_topic_idx
  ON testagram_local.topic_follows(topic_id, created_at DESC, user_id);

CREATE TABLE IF NOT EXISTS testagram_local.question_create_idempotency (
  author_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE RESTRICT,
  key_hash TEXT NOT NULL CHECK (key_hash ~ '^[0-9a-f]{64}$'),
  request_hash TEXT NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
  question_id UUID NOT NULL REFERENCES testagram_local.questions(id) ON DELETE RESTRICT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY(author_id, key_hash)
);

CREATE TABLE IF NOT EXISTS testagram_local.answer_create_idempotency (
  author_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE RESTRICT,
  key_hash TEXT NOT NULL CHECK (key_hash ~ '^[0-9a-f]{64}$'),
  request_hash TEXT NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
  answer_id UUID NOT NULL REFERENCES testagram_local.answers(id) ON DELETE RESTRICT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY(author_id, key_hash)
);

CREATE OR REPLACE FUNCTION testagram_local.guard_question_acceptance()
RETURNS trigger
LANGUAGE plpgsql
AS $function$
DECLARE answer_author UUID; answer_deleted_at TIMESTAMPTZ;
BEGIN
  IF NEW.accepted_answer_id IS NULL THEN
    IF NEW.status='answered' THEN
      RAISE EXCEPTION 'answered question requires an accepted answer';
    END IF;
    RETURN NEW;
  END IF;

  IF NEW.status NOT IN ('answered','closed') OR NEW.status='removed' THEN
    RAISE EXCEPTION 'accepted answer requires an answered or closed question';
  END IF;

  SELECT author_id,deleted_at
    INTO answer_author,answer_deleted_at
  FROM testagram_local.answers
  WHERE id=NEW.accepted_answer_id AND question_id=NEW.id
  FOR UPDATE;

  IF NOT FOUND OR answer_deleted_at IS NOT NULL THEN
    RAISE EXCEPTION 'accepted answer must be a live answer belonging to this question';
  END IF;
  IF answer_author=NEW.author_id THEN
    RAISE EXCEPTION 'question authors cannot accept their own answer';
  END IF;
  RETURN NEW;
END;
$function$;

DROP TRIGGER IF EXISTS testagram_question_acceptance_guard ON testagram_local.questions;
CREATE TRIGGER testagram_question_acceptance_guard
BEFORE INSERT OR UPDATE OF accepted_answer_id,status,author_id
ON testagram_local.questions
FOR EACH ROW EXECUTE FUNCTION testagram_local.guard_question_acceptance();

CREATE OR REPLACE FUNCTION testagram_local.guard_accepted_answer_soft_delete()
RETURNS trigger
LANGUAGE plpgsql
AS $function$
BEGIN
  IF OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL
     AND EXISTS (
       SELECT 1 FROM testagram_local.questions q
       WHERE q.id=OLD.question_id AND q.accepted_answer_id=OLD.id
     ) THEN
    RAISE EXCEPTION 'clear accepted answer before soft-deleting the answer';
  END IF;
  RETURN NEW;
END;
$function$;

DROP TRIGGER IF EXISTS testagram_accepted_answer_delete_guard ON testagram_local.answers;
CREATE TRIGGER testagram_accepted_answer_delete_guard
BEFORE UPDATE OF deleted_at ON testagram_local.answers
FOR EACH ROW EXECUTE FUNCTION testagram_local.guard_accepted_answer_soft_delete();

CREATE OR REPLACE FUNCTION testagram_local.reject_answer_revision_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $function$
BEGIN
  RAISE EXCEPTION 'answer revisions are immutable audit history';
END;
$function$;

DROP TRIGGER IF EXISTS testagram_answer_revision_immutable ON testagram_local.answer_revisions;
CREATE TRIGGER testagram_answer_revision_immutable
BEFORE UPDATE OR DELETE ON testagram_local.answer_revisions
FOR EACH ROW EXECUTE FUNCTION testagram_local.reject_answer_revision_mutation();

REVOKE ALL ON
  testagram_local.topics,
  testagram_local.questions,
  testagram_local.answers,
  testagram_local.answer_revisions,
  testagram_local.question_topics,
  testagram_local.topic_follows,
  testagram_local.question_create_idempotency,
  testagram_local.answer_create_idempotency
FROM PUBLIC;

DO $grants$
DECLARE role_name TEXT;
BEGIN
  FOREACH role_name IN ARRAY ARRAY['testagram_app','service_role'] LOOP
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname=role_name) THEN
      EXECUTE format('GRANT USAGE ON SCHEMA testagram_local TO %I', role_name);
      EXECUTE format('GRANT SELECT ON testagram_local.topics TO %I', role_name);
      EXECUTE format('GRANT SELECT,INSERT,UPDATE ON testagram_local.questions,testagram_local.answers,testagram_local.question_topics,testagram_local.topic_follows,testagram_local.question_create_idempotency,testagram_local.answer_create_idempotency TO %I', role_name);
      EXECUTE format('GRANT SELECT,INSERT ON testagram_local.answer_revisions TO %I', role_name);
    END IF;
  END LOOP;
END;
$grants$;

COMMIT;
