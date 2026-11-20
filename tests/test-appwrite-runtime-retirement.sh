#!/usr/bin/env bash
set -Eeuo pipefail
: "${DATABASE_URL:?DATABASE_URL is required}"
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 <<'SQL'
DO $roles$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN CREATE ROLE testagram_app NOLOGIN; END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN CREATE ROLE service_role NOLOGIN; END IF;
END;
$roles$;
DROP SCHEMA IF EXISTS testagram_identity CASCADE;
CREATE SCHEMA testagram_identity;
CREATE TABLE testagram_identity.appwrite_accounts(id bigint PRIMARY KEY, legacy_id text NOT NULL);
CREATE SEQUENCE testagram_identity.legacy_sequence;
INSERT INTO testagram_identity.appwrite_accounts VALUES (1,'preserve-me');
GRANT ALL ON SCHEMA testagram_identity TO testagram_app,service_role;
GRANT ALL ON ALL TABLES IN SCHEMA testagram_identity TO testagram_app,service_role;
GRANT ALL ON ALL SEQUENCES IN SCHEMA testagram_identity TO testagram_app,service_role;
SQL

psql "$DATABASE_URL" -v ON_ERROR_STOP=1 < migrations/20261122_retire_appwrite_identity_runtime.sql

psql "$DATABASE_URL" -v ON_ERROR_STOP=1 <<'SQL'
CREATE TABLE testagram_identity.created_after_retirement(id bigint);
DO $verify$
BEGIN
  IF has_schema_privilege('testagram_app','testagram_identity','USAGE')
     OR has_schema_privilege('service_role','testagram_identity','USAGE') THEN
    RAISE EXCEPTION 'application/service role retains legacy schema access';
  END IF;
  IF has_table_privilege('testagram_app','testagram_identity.appwrite_accounts','SELECT')
     OR has_table_privilege('service_role','testagram_identity.appwrite_accounts','INSERT') THEN
    RAISE EXCEPTION 'application/service role retains legacy table access';
  END IF;
  IF has_sequence_privilege('testagram_app','testagram_identity.legacy_sequence','USAGE')
     OR has_sequence_privilege('service_role','testagram_identity.legacy_sequence','UPDATE') THEN
    RAISE EXCEPTION 'application/service role retains legacy sequence access';
  END IF;
  IF has_table_privilege('testagram_app','testagram_identity.created_after_retirement','SELECT')
     OR has_table_privilege('service_role','testagram_identity.created_after_retirement','INSERT') THEN
    RAISE EXCEPTION 'future legacy table inherited application/service privileges';
  END IF;
  IF (SELECT count(*) FROM testagram_identity.appwrite_accounts WHERE legacy_id='preserve-me')<>1 THEN
    RAISE EXCEPTION 'retirement must preserve historical identity evidence';
  END IF;
END;
$verify$;
SQL
