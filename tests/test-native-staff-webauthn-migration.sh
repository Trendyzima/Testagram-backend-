#!/usr/bin/env bash
set -Eeuo pipefail
: "${DATABASE_URL:?DATABASE_URL is required}"
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 <<'SQL'
CREATE ROLE testagram_app NOLOGIN;
CREATE ROLE service_role NOLOGIN;
CREATE SCHEMA testagram_auth;
CREATE TABLE testagram_auth.staff_principals (
  user_id uuid PRIMARY KEY,
  enabled boolean NOT NULL DEFAULT true,
  mfa_enrolled_at timestamptz
);
SQL
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 < migrations/20261121_native_staff_webauthn.sql
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 <<'SQL'
INSERT INTO testagram_auth.staff_principals(user_id) VALUES
 ('11111111-1111-4111-8111-111111111111'),
 ('22222222-2222-4222-8222-222222222222');
INSERT INTO testagram_auth.staff_webauthn_enrollment_authorizations
 (user_id,authorization_method,reason,expires_at)
 VALUES ('11111111-1111-4111-8111-111111111111','operator_bootstrap',
         'Initial owner bootstrap authorization',now()+interval '30 minutes');
INSERT INTO testagram_auth.staff_webauthn_enrollment_authorizations
 (user_id,authorized_by,authorization_method,reason,expires_at)
 VALUES ('22222222-2222-4222-8222-222222222222',
         '11111111-1111-4111-8111-111111111111','dual_control',
         'Second staff identity independently authorized',now()+interval '30 minutes');
DO $verify$
BEGIN
 IF NOT has_table_privilege('testagram_app','testagram_auth.staff_webauthn_credentials','INSERT')
    OR NOT has_table_privilege('testagram_app','testagram_auth.staff_webauthn_challenges','DELETE') THEN
   RAISE EXCEPTION 'application role is missing WebAuthn storage privileges';
 END IF;
 IF (SELECT count(*) FROM testagram_auth.staff_webauthn_enrollment_authorizations WHERE consumed_at IS NULL)<>2 THEN
   RAISE EXCEPTION 'expected two independent enrollment authorization rows';
 END IF;
 IF NOT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname='testagram_auth' AND indexname='staff_webauthn_credentials_user_idx')
    OR NOT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname='testagram_auth' AND indexname='staff_webauthn_challenges_expiry_idx') THEN
   RAISE EXCEPTION 'WebAuthn lookup/expiry indexes are missing';
 END IF;
END;
$verify$;
SQL
if psql "$DATABASE_URL" -v ON_ERROR_STOP=1 <<'SQL'
INSERT INTO testagram_auth.staff_webauthn_enrollment_authorizations
 (user_id,authorized_by,authorization_method,reason)
 VALUES ('11111111-1111-4111-8111-111111111111',
         '11111111-1111-4111-8111-111111111111','dual_control',
         'Self authorization must fail');
SQL
then
  echo "self-authorized enrollment unexpectedly succeeded" >&2
  exit 1
fi
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 <<'SQL'
INSERT INTO testagram_auth.staff_webauthn_challenges
 (challenge_hash,user_id,ceremony,session_data,expires_at)
 VALUES (decode(repeat('ab',32),'hex'),'11111111-1111-4111-8111-111111111111',
         'assertion',decode('00','hex'),now()+interval '2 minutes');
UPDATE testagram_auth.staff_webauthn_challenges SET expires_at=now()-interval '1 second'
 WHERE challenge_hash=decode(repeat('ab',32),'hex');
DO $verify$
BEGIN
 IF EXISTS (SELECT 1 FROM testagram_auth.staff_webauthn_challenges
            WHERE challenge_hash=decode(repeat('ab',32),'hex') AND expires_at>now()) THEN
   RAISE EXCEPTION 'expired challenge was considered live';
 END IF;
END;
$verify$;
SQL
