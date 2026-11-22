#!/usr/bin/env bash
set -euo pipefail
: "${DATABASE_URL:?DATABASE_URL is required}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 <<'SQL'
DO $roles$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN CREATE ROLE testagram_app NOLOGIN; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN CREATE ROLE service_role NOLOGIN; END IF;
END
$roles$;
CREATE SCHEMA IF NOT EXISTS testagram_local;
CREATE TABLE IF NOT EXISTS testagram_local.accounts (user_id uuid PRIMARY KEY);
SQL
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f "$root/migrations/062_testagram_knowledge_engine.sql"
# Replaying the additive migration must be safe.
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f "$root/migrations/062_testagram_knowledge_engine.sql"
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 <<'SQL'
INSERT INTO testagram_local.accounts(user_id) VALUES
 ('00000000-0000-4000-8000-000000000001'),
 ('00000000-0000-4000-8000-000000000002'),
 ('00000000-0000-4000-8000-000000000003')
ON CONFLICT DO NOTHING;

INSERT INTO testagram_local.topics(id,slug,name)
VALUES ('10000000-0000-4000-8000-000000000001','postgresql','PostgreSQL')
ON CONFLICT DO NOTHING;

INSERT INTO testagram_local.questions(id,author_id,title,body)
VALUES
 ('20000000-0000-4000-8000-000000000001','00000000-0000-4000-8000-000000000001','How do transactions work?','Explain isolation levels.'),
 ('20000000-0000-4000-8000-000000000002','00000000-0000-4000-8000-000000000002','How do indexes work?','Explain index scans.')
ON CONFLICT DO NOTHING;

INSERT INTO testagram_local.question_topics(question_id,topic_id,added_by)
VALUES ('20000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000001','00000000-0000-4000-8000-000000000001')
ON CONFLICT DO NOTHING;
INSERT INTO testagram_local.topic_follows(user_id,topic_id)
VALUES ('00000000-0000-4000-8000-000000000003','10000000-0000-4000-8000-000000000001')
ON CONFLICT DO NOTHING;

INSERT INTO testagram_local.answers(id,question_id,author_id,body)
VALUES
 ('30000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001','00000000-0000-4000-8000-000000000002','Use a transaction boundary.'),
 ('30000000-0000-4000-8000-000000000002','20000000-0000-4000-8000-000000000002','00000000-0000-4000-8000-000000000003','An index narrows the search.'),
 ('30000000-0000-4000-8000-000000000003','20000000-0000-4000-8000-000000000001','00000000-0000-4000-8000-000000000001','An author answer cannot be accepted.')
ON CONFLICT DO NOTHING;

INSERT INTO testagram_local.answer_revisions(answer_id,editor_id,revision_number,previous_body,body)
VALUES ('30000000-0000-4000-8000-000000000001','00000000-0000-4000-8000-000000000002',1,'','Use a transaction boundary.')
ON CONFLICT DO NOTHING;

UPDATE testagram_local.questions
SET accepted_answer_id='30000000-0000-4000-8000-000000000001',status='answered',version=version+1
WHERE id='20000000-0000-4000-8000-000000000001';

DO $invariants$
DECLARE rejected BOOLEAN;
BEGIN
  -- Cross-question acceptance must fail at the database boundary.
  rejected := FALSE;
  BEGIN
    UPDATE testagram_local.questions
    SET accepted_answer_id='30000000-0000-4000-8000-000000000002',status='answered'
    WHERE id='20000000-0000-4000-8000-000000000001';
  EXCEPTION WHEN raise_exception THEN rejected := TRUE;
  END;
  IF NOT rejected THEN RAISE EXCEPTION 'cross-question accepted answer was not rejected'; END IF;

  -- The question author cannot accept their own answer.
  rejected := FALSE;
  BEGIN
    UPDATE testagram_local.questions
    SET accepted_answer_id='30000000-0000-4000-8000-000000000003',status='answered'
    WHERE id='20000000-0000-4000-8000-000000000001';
  EXCEPTION WHEN raise_exception THEN rejected := TRUE;
  END;
  IF NOT rejected THEN RAISE EXCEPTION 'self-acceptance was not rejected'; END IF;

  -- A deleted answer cannot remain accepted; clear acceptance first.
  rejected := FALSE;
  BEGIN
    UPDATE testagram_local.answers SET deleted_at=now()
    WHERE id='30000000-0000-4000-8000-000000000001';
  EXCEPTION WHEN raise_exception THEN rejected := TRUE;
  END;
  IF NOT rejected THEN RAISE EXCEPTION 'soft deletion of accepted answer was not rejected'; END IF;

  UPDATE testagram_local.questions
  SET accepted_answer_id=NULL,status='open',version=version+1
  WHERE id='20000000-0000-4000-8000-000000000001';
  UPDATE testagram_local.answers SET deleted_at=now()
  WHERE id='30000000-0000-4000-8000-000000000001';

  -- Answer revisions are immutable.
  rejected := FALSE;
  BEGIN
    UPDATE testagram_local.answer_revisions SET body='tampered'
    WHERE answer_id='30000000-0000-4000-8000-000000000001';
  EXCEPTION WHEN raise_exception THEN rejected := TRUE;
  END;
  IF NOT rejected THEN RAISE EXCEPTION 'answer revision mutation was not rejected'; END IF;

  IF NOT has_table_privilege('testagram_app','testagram_local.questions','INSERT')
     OR NOT has_table_privilege('service_role','testagram_local.answers','SELECT')
     OR has_table_privilege('testagram_app','testagram_local.answer_revisions','UPDATE') THEN
    RAISE EXCEPTION 'knowledge engine Backend grants are incorrect';
  END IF;
  IF EXISTS (
    SELECT 1 FROM pg_class c CROSS JOIN LATERAL aclexplode(c.relacl) a
    WHERE c.oid='testagram_local.questions'::regclass AND a.grantee=0
  ) THEN
    RAISE EXCEPTION 'PUBLIC unexpectedly has privileges on questions';
  END IF;
END
$invariants$;

INSERT INTO testagram_local.question_create_idempotency(author_id,key_hash,request_hash,question_id)
VALUES ('00000000-0000-4000-8000-000000000001',repeat('a',64),repeat('b',64),'20000000-0000-4000-8000-000000000001');

DO $idempotency$
DECLARE rejected BOOLEAN := FALSE;
BEGIN
  BEGIN
    INSERT INTO testagram_local.question_create_idempotency(author_id,key_hash,request_hash,question_id)
    VALUES ('00000000-0000-4000-8000-000000000001',repeat('a',64),repeat('c',64),'20000000-0000-4000-8000-000000000001');
  EXCEPTION WHEN unique_violation THEN rejected := TRUE;
  END;
  IF NOT rejected THEN RAISE EXCEPTION 'question idempotency key uniqueness was not enforced'; END IF;
END
$idempotency$;
SQL
