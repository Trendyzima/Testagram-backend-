#!/usr/bin/env bash
set -euo pipefail

container="testagram-identity-registration-ci"
cleanup() { docker rm -f "$container" >/dev/null 2>&1 || true; }
trap cleanup EXIT

docker run -d --rm --name "$container" \
  -e POSTGRES_PASSWORD=ci-only-password \
  -p 5432:5432 postgres:17-alpine >/dev/null

for attempt in $(seq 1 30); do
  if docker exec "$container" pg_isready -U postgres >/dev/null 2>&1; then
    break
  fi
  if [[ "$attempt" -eq 30 ]]; then
    docker logs "$container"
    exit 1
  fi
  sleep 2
done

psql_ci() {
  docker exec -i "$container" psql -U postgres -v ON_ERROR_STOP=1 "$@"
}

psql_clean() {
  docker exec -i "$container" psql -U postgres -d native_clean_archive_test -v ON_ERROR_STOP=1 "$@"
}

psql_ci <<'SQL'
CREATE ROLE testagram_app NOLOGIN;
CREATE ROLE service_role NOLOGIN;
CREATE EXTENSION IF NOT EXISTS pgcrypto;
SQL

for migration in \
  migrations/20261019_testagram_independent_auth.sql \
  migrations/20261106_appwrite_identity_bridge.sql \
  migrations/20261106_appwrite_identity_link_audit.sql \
  migrations/20261114_appwrite_registration_consent.sql \
  migrations/20261110_staff_roles.sql \
  migrations/20261111_admin_security_controls.sql \
  migrations/20261111_identity_registration_pipeline.sql \
  migrations/20261113_identity_registration_hardening.sql \
  migrations/20261113_native_identity_verification.sql \
  migrations/20261114_native_identity_evidence.sql \
  migrations/20261114_native_identity_integrity.sql \
  migrations/20261115_identity_evidence_access_audit.sql \
  migrations/20261115_native_identity_status_access.sql \
  migrations/20261019_dual_backend_replication.sql \
  migrations/20261019_testagram_local_accounts_posts.sql \
  migrations/20261106_social_core.sql \
  migrations/20261115_testagram_native_auth_cutover_foundation.sql \
  migrations/20261116_native_approved_identity_provisioning.sql \
  migrations/20261116_native_auth_runtime_grants.sql \
  migrations/20261117_native_staff_reviewer_grants.sql \
  migrations/20261118_native_admin_console.sql \
  migrations/20261119_native_identity_approval_ledger.sql \
  migrations/20261120_native_identity_approval_circuit.sql \
  migrations/20261120_native_verification_decider.sql \
  migrations/20261121_native_identity_intake_optional_fingerprint.sql \
  migrations/20261121_native_staff_webauthn.sql \
  migrations/061_native_sensitive_action_integrity.sql \
  migrations/20261124_testagram_email_verification_otp.sql; do
  psql_ci < "$migration"
done

# Re-apply the new migration to prove idempotent DDL behavior.
psql_ci < migrations/20261113_identity_registration_hardening.sql
psql_ci < migrations/20261113_native_identity_verification.sql
psql_ci < migrations/20261113_native_identity_verification.sql
psql_ci < migrations/20261114_native_identity_evidence.sql
psql_ci < migrations/20261114_native_identity_evidence.sql
psql_ci < migrations/20261114_native_identity_integrity.sql
psql_ci < migrations/20261114_native_identity_integrity.sql
psql_ci < migrations/20261115_identity_evidence_access_audit.sql
psql_ci < migrations/20261115_identity_evidence_access_audit.sql
psql_ci < migrations/20261115_native_identity_status_access.sql
psql_ci < migrations/20261115_native_identity_status_access.sql
psql_ci < migrations/20261115_testagram_native_auth_cutover_foundation.sql
psql_ci < migrations/20261115_testagram_native_auth_cutover_foundation.sql
psql_ci < migrations/20261116_native_approved_identity_provisioning.sql
psql_ci < migrations/20261116_native_approved_identity_provisioning.sql
psql_ci < migrations/20261116_native_auth_runtime_grants.sql
psql_ci < migrations/20261116_native_auth_runtime_grants.sql
psql_ci < migrations/20261117_native_staff_reviewer_grants.sql
psql_ci < migrations/20261117_native_staff_reviewer_grants.sql
psql_ci < migrations/20261118_native_admin_console.sql
psql_ci < migrations/20261118_native_admin_console.sql
psql_ci < migrations/20261119_native_identity_approval_ledger.sql
psql_ci < migrations/20261119_native_identity_approval_ledger.sql
psql_ci < migrations/20261120_native_identity_approval_circuit.sql
psql_ci < migrations/20261120_native_identity_approval_circuit.sql
psql_ci < migrations/20261120_native_verification_decider.sql
psql_ci < migrations/20261121_native_identity_intake_optional_fingerprint.sql
psql_ci < migrations/20261121_native_staff_webauthn.sql
psql_ci < migrations/061_native_sensitive_action_integrity.sql
psql_ci < migrations/061_native_sensitive_action_integrity.sql
psql_ci < migrations/20261122_testagram_mail_outbox.sql
psql_ci < migrations/20261122_testagram_mail_outbox.sql
psql_ci < migrations/20261106_social_core.sql
psql_ci < migrations/20261106_social_core.sql
psql_ci < migrations/20261124_testagram_social_email_consent.sql
psql_ci < migrations/20261125_testagram_mail_delivery_events.sql
psql_ci < migrations/20261126_testagram_native_mail_feedback.sql
psql_ci < migrations/20261124_testagram_email_verification_otp.sql
psql_ci < migrations/20261124_testagram_email_verification_otp.sql
psql_ci < migrations/20261124_testagram_social_email_consent.sql
psql_ci < migrations/20261125_testagram_mail_delivery_events.sql
psql_ci < migrations/20261126_testagram_native_mail_feedback.sql
psql_ci < migrations/20261124_testagram_social_email_consent.sql
psql_ci < migrations/20261125_testagram_mail_delivery_events.sql
psql_ci < migrations/20261126_testagram_native_mail_feedback.sql
psql_ci < migrations/20261120_native_verification_decider.sql
psql_ci < migrations/20261122_testagram_mail_outbox.sql
psql_ci < migrations/20261124_testagram_social_email_consent.sql
psql_ci < migrations/20261125_testagram_mail_delivery_events.sql
psql_ci < migrations/20261126_testagram_native_mail_feedback.sql

psql_ci <<'SQL'
INSERT INTO testagram_auth.identities(user_id,email,username,password_hash,email_verified_at) VALUES
  ('123e4567-e89b-42d3-a456-426614174101','native-reviewer-one@example.test','native_reviewer_one','test-only-invalid-password-hash',now()),
  ('123e4567-e89b-42d3-a456-426614174102','native-reviewer-two@example.test','native_reviewer_two','test-only-invalid-password-hash',now()),
  ('123e4567-e89b-42d3-a456-426614174103','native-reviewer-requester@example.test','native_reviewer_requester','test-only-invalid-password-hash',now()),
  ('123e4567-e89b-42d3-a456-426614174104','native-account-approver-one@example.test','native_account_approver_one','test-only-invalid-password-hash',now()),
  ('123e4567-e89b-42d3-a456-426614174105','native-account-approver-two@example.test','native_account_approver_two','test-only-invalid-password-hash',now());
INSERT INTO testagram_auth.staff_principals(user_id,enabled,mfa_enrolled_at,enabled_at,enabled_by) VALUES
  ('123e4567-e89b-42d3-a456-426614174101',TRUE,now(),now(),'123e4567-e89b-42d3-a456-426614174103'),
  ('123e4567-e89b-42d3-a456-426614174102',TRUE,now(),now(),'123e4567-e89b-42d3-a456-426614174103'),
  ('123e4567-e89b-42d3-a456-426614174103',TRUE,now(),now(),'123e4567-e89b-42d3-a456-426614174103'),
  ('123e4567-e89b-42d3-a456-426614174104',TRUE,now(),now(),'123e4567-e89b-42d3-a456-426614174103'),
  ('123e4567-e89b-42d3-a456-426614174105',TRUE,now(),now(),'123e4567-e89b-42d3-a456-426614174103');
INSERT INTO testagram_auth.staff_role_grants(user_id,role,granted_by) VALUES
  ('123e4567-e89b-42d3-a456-426614174101','administrator','123e4567-e89b-42d3-a456-426614174103'),
  ('123e4567-e89b-42d3-a456-426614174102','administrator','123e4567-e89b-42d3-a456-426614174103'),
  ('123e4567-e89b-42d3-a456-426614174103','operations','123e4567-e89b-42d3-a456-426614174103'),
  ('123e4567-e89b-42d3-a456-426614174104','administrator','123e4567-e89b-42d3-a456-426614174103'),
  ('123e4567-e89b-42d3-a456-426614174105','administrator','123e4567-e89b-42d3-a456-426614174103');
INSERT INTO testagram_auth.staff_webauthn_users(rpid,user_id,user_handle) VALUES
  ('ops.testagram.invalid','123e4567-e89b-42d3-a456-426614174104',digest('123e4567-e89b-42d3-a456-426614174104','sha256')),
  ('ops.testagram.invalid','123e4567-e89b-42d3-a456-426614174105',digest('123e4567-e89b-42d3-a456-426614174105','sha256'));
INSERT INTO testagram_auth.staff_webauthn_credentials(rpid,user_id,credential_id,credential,user_verified,clone_warning) VALUES
  ('ops.testagram.invalid','123e4567-e89b-42d3-a456-426614174104',digest('credential:123e4567-e89b-42d3-a456-426614174104','sha256'),decode('04','hex'),TRUE,FALSE),
  ('ops.testagram.invalid','123e4567-e89b-42d3-a456-426614174105',digest('credential:123e4567-e89b-42d3-a456-426614174105','sha256'),decode('05','hex'),TRUE,FALSE);

DO $registration$
DECLARE
  app_id uuid := gen_random_uuid();
  action_id uuid := gen_random_uuid();
  session_id uuid := gen_random_uuid();
  provisioning_id uuid;
  premature_token_rejected boolean := FALSE;
  mismatched_evidence_update_rejected boolean := FALSE;
  approver_mutation_rejected boolean := FALSE;
BEGIN
  INSERT INTO testagram_auth.native_sensitive_action_requests
    (id,action_type,requested_by_user_id,requested_by_service,target_reference,reason,payload,payload_sha256)
  VALUES
    (action_id,'identity.approve',NULL,'identity_pipeline',app_id::text,
     'CI registration approval request',jsonb_build_object('application_id',app_id::text,'email','first@example.test',
       'username','ci_first','display_name','CI First',
       'evidence_fingerprint_sha256',repeat('1',64),
       'evidence_audit_reference','ci-evidence-audit-reference',
       'terms_version','terms-v1','privacy_version','privacy-v1'),repeat('a',64));

  INSERT INTO testagram_identity.identity_applications
    (id,email,username,display_name,adult_confirmed_at,adult_verified_at,
     adult_verification_provider,adult_verification_reference,adult_verification_checked_at,
     evidence_reference,evidence_fingerprint_sha256,terms_version,privacy_version,
     terms_accepted_at,privacy_accepted_at,submission_key_sha256,status,native_sensitive_action_request_id)
  VALUES
    (app_id,'first@example.test','ci_first','CI First',now(),now(),
     'testagram_manual','ci-adult-evidence-ref',now(),
     'opaque-ci-evidence-ref',decode(repeat('1',64),'hex'),'terms-v1','privacy-v1',now(),now(),
     decode(repeat('2',64),'hex'),'verified',action_id);

  -- Approval token guards require evidence to be linked to a verified
  -- age-assurance session, even in this pending-approval fixture.
  INSERT INTO testagram_identity.verification_sessions
    (id,application_id,session_token_sha256,purpose,requested_minimum_age,status,
     evidence_reference,evidence_fingerprint_sha256,assurance_method,assurance_level,
     verified_age_over_threshold,expires_at,submitted_at,decided_at,decided_by_user_id)
  VALUES
    (session_id,app_id,decode(repeat('5',64),'hex'),'age_assurance',18,'verified',
     'opaque-ci-evidence-ref',decode(repeat('1',64),'hex'),'document_review','reviewed',
     TRUE,now()+interval '1 day',now(),now(),
     '123e4567-e89b-42d3-a456-426614174101');

  -- A token cannot be issued while the ledger is still pending.
  BEGIN
    BEGIN
      INSERT INTO testagram_identity.identity_approval_tokens
        (token_sha256,jti_sha256,application_id,first_approver_user_id,
         second_approver_user_id,expires_at)
      VALUES
        (decode(repeat('a',64),'hex'),decode(repeat('b',64),'hex'),app_id,
         '123e4567-e89b-42d3-a456-426614174104','123e4567-e89b-42d3-a456-426614174105',now()+interval '10 minutes');
    EXCEPTION WHEN raise_exception THEN
      premature_token_rejected := TRUE;
    END;
    IF NOT premature_token_rejected THEN
      RAISE EXCEPTION 'token issuance before two-person approval was accepted';
    END IF;
  END;

  INSERT INTO testagram_identity.identity_application_reviews
    (application_id,reviewer_user_id,review_stage,decision,reason,evidence_audit_reference)
  VALUES
    (app_id,'123e4567-e89b-42d3-a456-426614174101','evidence_review','approved','First evidence review','ci-evidence-audit-reference'),
    (app_id,'123e4567-e89b-42d3-a456-426614174102','evidence_review','approved','Second evidence review','ci-evidence-audit-reference'),
    (app_id,'123e4567-e89b-42d3-a456-426614174104','account_approval','approved','First independent approval','account-audit-reference-0001'),
    (app_id,'123e4567-e89b-42d3-a456-426614174105','account_approval','approved','Second independent approval','account-audit-reference-0002');

  INSERT INTO testagram_auth.native_sensitive_action_approvals
    (request_id,approver_user_id,decision,reason,webauthn_rpid,webauthn_credential_id)
  VALUES
    (action_id,'123e4567-e89b-42d3-a456-426614174104','approved','First independent approval','ops.testagram.invalid',digest('credential:123e4567-e89b-42d3-a456-426614174104','sha256')),
    (action_id,'123e4567-e89b-42d3-a456-426614174105','approved','Second independent approval','ops.testagram.invalid',digest('credential:123e4567-e89b-42d3-a456-426614174105','sha256'));

  UPDATE testagram_auth.native_sensitive_action_requests
  SET status='approved',decided_at=now()
  WHERE id=action_id;
  UPDATE testagram_identity.identity_applications
  SET status='approved'
  WHERE id=app_id;

  INSERT INTO testagram_identity.identity_approval_tokens
    (token_sha256,jti_sha256,application_id,first_approver_user_id,
     second_approver_user_id,expires_at)
  VALUES
    (decode(repeat('3',64),'hex'),decode(repeat('4',64),'hex'),app_id,
     '123e4567-e89b-42d3-a456-426614174104',
     '123e4567-e89b-42d3-a456-426614174105',now()+interval '10 minutes');

  -- The token trigger must fire on UPDATE, not just INSERT. First make
  -- one evidence-review record point at a different audit reference and prove
  -- that even a no-op signer update revalidates the full evidence quorum.
  UPDATE testagram_identity.identity_application_reviews
  SET evidence_audit_reference='mismatched-evidence-audit-reference'
  WHERE application_id=app_id AND reviewer_user_id='123e4567-e89b-42d3-a456-426614174102'
    AND review_stage='evidence_review';

  BEGIN
    UPDATE testagram_identity.identity_approval_tokens
    SET second_approver_user_id=second_approver_user_id
    WHERE application_id=app_id;
    RAISE EXCEPTION 'approval token update accepted mismatched evidence audit reference';
  EXCEPTION WHEN raise_exception THEN
    IF SQLERRM='approval token update accepted mismatched evidence audit reference' THEN RAISE; END IF;
    mismatched_evidence_update_rejected := TRUE;
  END;
  IF NOT mismatched_evidence_update_rejected THEN
    RAISE EXCEPTION 'approval token update did not enforce matching evidence references';
  END IF;

  UPDATE testagram_identity.identity_application_reviews
  SET evidence_audit_reference='ci-evidence-audit-reference'
  WHERE application_id=app_id AND reviewer_user_id='123e4567-e89b-42d3-a456-426614174102'
    AND review_stage='evidence_review';

  -- An evidence reviewer must not be substituted into the account-approver
  -- pair after token creation.
  BEGIN
    UPDATE testagram_identity.identity_approval_tokens
    SET second_approver_user_id='123e4567-e89b-42d3-a456-426614174102'
    WHERE application_id=app_id;
    RAISE EXCEPTION 'approval token signer mutation accepted an evidence reviewer';
  EXCEPTION WHEN raise_exception THEN
    IF SQLERRM='approval token signer mutation accepted an evidence reviewer' THEN RAISE; END IF;
    approver_mutation_rejected := TRUE;
  END;
  IF NOT approver_mutation_rejected THEN
    RAISE EXCEPTION 'approval token signer mutation did not enforce reviewer/approver separation';
  END IF;

  INSERT INTO testagram_identity.registration_provisioning
    (application_id,idempotency_key_sha256)
  VALUES (app_id,decode(repeat('5',64),'hex'))
  RETURNING id INTO provisioning_id;

  UPDATE testagram_identity.registration_provisioning
  SET lease_owner='ci-worker',lease_expires_at=now()+interval '1 minute',
      last_attempt_at=now()
  WHERE id=provisioning_id;

  INSERT INTO testagram_identity.legal_consent_audit
    (application_id,consent_type,policy_version,accepted_at,request_id)
  VALUES
    (app_id,'terms','terms-v1',now(),'ci-registration-request'),
    (app_id,'privacy','privacy-v1',now(),'ci-registration-request'),
    (app_id,'age_confirmation','age-v1',now(),'ci-registration-request');

  IF NOT EXISTS (
    SELECT 1 FROM testagram_identity.identity_applications
    WHERE id=app_id AND native_sensitive_action_request_id=action_id
  ) THEN
    RAISE EXCEPTION 'application-to-approval-ledger link missing';
  END IF;
  IF (SELECT count(*) FROM testagram_identity.identity_application_reviews WHERE application_id=app_id AND decision='approved' AND review_stage='evidence_review') <> 2
     OR (SELECT count(*) FROM testagram_identity.identity_application_reviews WHERE application_id=app_id AND decision='approved' AND review_stage='account_approval') <> 2 THEN
    RAISE EXCEPTION 'two evidence reviewers and two separate account approvers were not persisted';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM testagram_identity.registration_provisioning
    WHERE id=provisioning_id AND lease_owner='ci-worker' AND lease_expires_at IS NOT NULL
  ) THEN
    RAISE EXCEPTION 'provisioning lease was not persisted';
  END IF;
END;
$registration$;

-- Case-insensitive active email and username uniqueness must reject duplicates.
DO $unique_checks$
BEGIN
  BEGIN
    INSERT INTO testagram_identity.identity_applications
      (email,username,display_name,adult_confirmed_at,evidence_reference,
       evidence_fingerprint_sha256,terms_version,privacy_version,
       terms_accepted_at,privacy_accepted_at)
    VALUES
      ('first@example.test','other_user','Duplicate email',now(),'opaque-ci-evidence-ref-2',
       decode(repeat('6',64),'hex'),'terms-v1','privacy-v1',now(),now());
    RAISE EXCEPTION 'active duplicate email was accepted';
  EXCEPTION WHEN unique_violation THEN
    NULL;
  END;

  BEGIN
    INSERT INTO testagram_identity.identity_applications
      (email,username,display_name,adult_confirmed_at,evidence_reference,
       evidence_fingerprint_sha256,terms_version,privacy_version,
       terms_accepted_at,privacy_accepted_at)
    VALUES
      ('other@example.test','CI_FIRST','Duplicate username',now(),'opaque-ci-evidence-ref-3',
       decode(repeat('7',64),'hex'),'terms-v1','privacy-v1',now(),now());
    RAISE EXCEPTION 'active duplicate username was accepted';
  EXCEPTION WHEN unique_violation THEN
    NULL;
  END;
END;
$unique_checks$;

-- Provider evidence is mandatory when adult_verified_at is set.
DO $adult_evidence$
BEGIN
  BEGIN
    INSERT INTO testagram_identity.identity_applications
      (email,username,display_name,adult_confirmed_at,adult_verified_at,
       evidence_reference,evidence_fingerprint_sha256,terms_version,privacy_version,
       terms_accepted_at,privacy_accepted_at)
    VALUES
      ('adult-bad@example.test','adult_bad','Bad adult evidence',now(),now(),
       'opaque-ci-evidence-ref-4',decode(repeat('8',64),'hex'),
       'terms-v1','privacy-v1',now(),now());
    RAISE EXCEPTION 'adult verification without provider evidence was accepted';
  EXCEPTION WHEN check_violation THEN
    NULL;
  END;
END;
$adult_evidence$;

-- A half-populated lease must be rejected.
DO $lease_invariant$
DECLARE
  app_id uuid;
  provision_id uuid;
BEGIN
  SELECT id INTO app_id FROM testagram_identity.identity_applications
  WHERE email='first@example.test';
  SELECT id INTO provision_id
  FROM testagram_identity.registration_provisioning
  WHERE application_id=app_id;

  UPDATE testagram_identity.registration_provisioning
  SET lease_owner=NULL,lease_expires_at=NULL
  WHERE id=provision_id;

  BEGIN
    UPDATE testagram_identity.registration_provisioning
    SET lease_owner='orphaned-worker'
    WHERE id=provision_id;
    RAISE EXCEPTION 'half-populated provisioning lease was accepted';
  EXCEPTION WHEN check_violation THEN
    NULL;
  END;
END;
$lease_invariant$;
SQL

# Exercise native verification session constraints and immutable audit records.
psql_ci <<'SQL'
DO $native_identity$
DECLARE
  app_id uuid := gen_random_uuid();
  v_session_id uuid := gen_random_uuid();
BEGIN
  INSERT INTO testagram_identity.identity_applications
    (id,email,username,display_name,adult_confirmed_at,evidence_reference,
     evidence_fingerprint_sha256,terms_version,privacy_version,
     terms_accepted_at,privacy_accepted_at)
  VALUES
    (app_id,'native@example.test','native_ci','Native CI',now(),
     'native-pending-reference',decode(repeat('9',64),'hex'),
     'terms-v1','privacy-v1',now(),now());

  INSERT INTO testagram_identity.verification_sessions
    (id,application_id,session_token_sha256,purpose,requested_minimum_age,expires_at)
  VALUES
    (v_session_id,app_id,decode(repeat('a',64),'hex'),'age_assurance',18,now()+interval '1 day');

  -- Identity assurance must not require an age-threshold result.
  INSERT INTO testagram_identity.verification_sessions
    (id,application_id,session_token_sha256,purpose,requested_minimum_age,status,
     evidence_reference,evidence_fingerprint_sha256,assurance_method,assurance_level,
     verified_age_over_threshold,expires_at,submitted_at,decided_at,decided_by_user_id)
  VALUES
    (gen_random_uuid(),app_id,decode(repeat('b',64),'hex'),'identity_assurance',NULL,'verified',
     'opaque-identity-evidence',decode(repeat('c',64),'hex'),'document_review','reviewed',
     NULL,now()+interval '1 day',now(),now(),'123e4567-e89b-42d3-a456-426614174102');

  INSERT INTO testagram_identity.verification_events
    (session_id,event_type,actor_type,actor_reference,request_id)
  VALUES
    (v_session_id,'session_created','applicant','native-ci','native-session-created'),
    (v_session_id,'evidence_access_attempted','reviewer','ci-reviewer-one','native-evidence-read');

  IF NOT EXISTS (
    SELECT 1 FROM testagram_identity.verification_events
    WHERE session_id=v_session_id AND event_type='evidence_access_attempted'
      AND actor_type='reviewer' AND actor_reference='ci-reviewer-one'
  ) THEN
    RAISE EXCEPTION 'reviewer evidence-access audit event was not persisted';
  END IF;

  INSERT INTO testagram_identity.verification_reviews
    (session_id,reviewer_user_id,decision,reason_code,evidence_reference)
  VALUES
    (v_session_id,'123e4567-e89b-42d3-a456-426614174101','escalate','needs_second_review','opaque-native-evidence-ref');

  BEGIN
    UPDATE testagram_identity.verification_events
    SET actor_reference='tampered' WHERE verification_events.session_id=v_session_id;
    RAISE EXCEPTION 'verification event update unexpectedly succeeded';
  EXCEPTION WHEN raise_exception THEN
    IF SQLERRM = 'verification event update unexpectedly succeeded' THEN RAISE; END IF;
  END;

  BEGIN
    DELETE FROM testagram_identity.verification_reviews
    WHERE verification_reviews.session_id=v_session_id;
    RAISE EXCEPTION 'verification review deletion unexpectedly succeeded';
  EXCEPTION WHEN raise_exception THEN
    IF SQLERRM = 'verification review deletion unexpectedly succeeded' THEN RAISE; END IF;
  END;

  BEGIN
    UPDATE testagram_identity.verification_sessions
    SET status='submitted',submitted_at=now()
    WHERE id=v_session_id;
    RAISE EXCEPTION 'evidence-free submission unexpectedly succeeded';
  EXCEPTION WHEN check_violation THEN
    NULL;
  END;
END;
$native_identity$;
SQL

# Regression: preserve historical lifecycle event types when the older
# evidence-access migration is replayed after those events have been written.
psql_ci <<'SQL'
DO $seed_historical_verification_events$
DECLARE
  session_id uuid;
BEGIN
  SELECT id INTO session_id
  FROM testagram_identity.verification_sessions
  LIMIT 1;

  IF session_id IS NULL THEN
    RAISE EXCEPTION 'verification session fixture is missing';
  END IF;

  INSERT INTO testagram_identity.verification_events
    (session_id,event_type,actor_type,actor_reference,request_id)
  VALUES
    (session_id,'account_provisioned','applicant','ci-native-auth','ci-account-provisioned'),
    (session_id,'email_verified','applicant','ci-native-auth','ci-email-verified');
END;
$seed_historical_verification_events$;
SQL
psql_ci < migrations/20261115_identity_evidence_access_audit.sql
psql_ci <<'SQL'
DO $verify_historical_verification_events$
BEGIN
  IF (
    SELECT count(*) FROM testagram_identity.verification_events
    WHERE event_type IN ('account_provisioned','email_verified')
      AND request_id IN ('ci-account-provisioned','ci-email-verified')
  ) <> 2 THEN
    RAISE EXCEPTION 'replaying evidence-access audit migration lost historical verification event types';
  END IF;
END;
$verify_historical_verification_events$;
SQL

# Exercise the evidence retention state machine used by the storage worker.
psql_ci <<'SQL'
DO $evidence_retention$
DECLARE
  app_id uuid := gen_random_uuid();
  session_id uuid := gen_random_uuid();
  evidence_id uuid := gen_random_uuid();
BEGIN
  INSERT INTO testagram_identity.identity_applications
    (id,email,username,display_name,adult_confirmed_at,evidence_reference,
     evidence_fingerprint_sha256,terms_version,privacy_version,
     terms_accepted_at,privacy_accepted_at)
  VALUES
    (app_id,'retention@example.test','retention_ci','Retention CI',now(),
     'native-retention-reference',decode(repeat('d',64),'hex'),
     'terms-v1','privacy-v1',now(),now());

  INSERT INTO testagram_identity.verification_sessions
    (id,application_id,session_token_sha256,purpose,requested_minimum_age,status,expires_at)
  VALUES
    (session_id,app_id,decode(repeat('e',64),'hex'),'age_assurance',18,'in_progress',now()+interval '1 day');

  INSERT INTO testagram_identity.verification_evidence
    (id,session_id,object_key,content_type,size_bytes,sha256,status,retention_until)
  VALUES
    (evidence_id,session_id,'identity/ci/evidence.pdf','application/pdf',10,
     decode(repeat('f',64),'hex'),'upload_pending',now()-interval '1 minute');

  UPDATE testagram_identity.verification_evidence
  SET status='quarantined',uploaded_at=now(),verified_at=NULL
  WHERE id=evidence_id;

  UPDATE testagram_identity.verification_evidence
  SET status='deleted',verified_at=NULL
  WHERE id=evidence_id AND status='quarantined';

  IF NOT EXISTS (
    SELECT 1 FROM testagram_identity.verification_evidence
    WHERE id=evidence_id AND status='deleted'
      AND uploaded_at IS NOT NULL AND verified_at IS NULL
  ) THEN
    RAISE EXCEPTION 'expired evidence did not reach the terminal deleted state';
  END IF;
END;
$evidence_retention$;
SQL

# Provide the canonical account table needed by the API provisioning integration.
psql_ci <<'SQL'
CREATE SCHEMA IF NOT EXISTS testagram_local;
CREATE TABLE IF NOT EXISTS testagram_local.accounts (
  user_id UUID PRIMARY KEY,
  username TEXT NOT NULL UNIQUE,
  display_name TEXT NOT NULL DEFAULT '',
  account_status TEXT NOT NULL DEFAULT 'active'
    CHECK (account_status IN ('active','deactivated','suspended')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
SQL

# Apply the complete native-auth schema only after the canonical local account table exists.
psql_ci < migrations/20261115_testagram_native_auth_cutover_foundation.sql
psql_ci < migrations/20261116_native_approved_identity_provisioning.sql
psql_ci < migrations/20261116_native_auth_runtime_grants.sql

# Re-apply all three migrations to verify idempotence and assert the exact runtime grant surface.
psql_ci < migrations/20261115_testagram_native_auth_cutover_foundation.sql
psql_ci < migrations/20261116_native_approved_identity_provisioning.sql
psql_ci < migrations/20261116_native_auth_runtime_grants.sql
psql_ci <<'SQL'
DO $native_auth_schema$
BEGIN
  IF to_regclass('testagram_auth.rate_limits') IS NULL
     OR to_regclass('testagram_auth.sessions') IS NULL
     OR to_regclass('testagram_auth.password_reset_tokens') IS NULL
     OR to_regclass('testagram_auth.security_audit') IS NULL
     OR to_regclass('testagram_auth.legacy_account_reconciliation') IS NULL
     OR to_regclass('testagram_identity.native_registration_provisioning') IS NULL
     OR to_regclass('testagram_mail.outbox') IS NULL THEN
    RAISE EXCEPTION 'native authentication schema is incomplete';
  END IF;
  IF NOT has_schema_privilege('testagram_app','testagram_mail','USAGE')
     OR NOT has_table_privilege('testagram_app','testagram_mail.outbox','SELECT')
     OR NOT has_table_privilege('testagram_app','testagram_mail.outbox','INSERT')
     OR NOT has_table_privilege('testagram_app','testagram_mail.outbox','UPDATE')
     OR NOT has_sequence_privilege('testagram_app','testagram_mail.outbox_id_seq','USAGE')
     OR NOT has_sequence_privilege('testagram_app','testagram_mail.outbox_id_seq','SELECT') THEN
    RAISE EXCEPTION 'Backend runtime role lacks transactional mail schema, table or sequence privileges';
  END IF;
  IF NOT has_table_privilege('testagram_app','testagram_auth.rate_limits','SELECT')
     OR NOT has_table_privilege('testagram_app','testagram_auth.rate_limits','INSERT')
     OR NOT has_table_privilege('testagram_app','testagram_auth.sessions','UPDATE')
     OR NOT has_table_privilege('testagram_app','testagram_auth.security_audit','INSERT') THEN
    RAISE EXCEPTION 'Backend runtime role lacks native auth grants';
  END IF;
  IF NOT has_table_privilege('testagram_app','testagram_auth.staff_role_grants','SELECT')
     OR NOT has_table_privilege('testagram_app','testagram_auth.staff_principals','SELECT') THEN
    RAISE EXCEPTION 'Backend runtime role lacks read access to native staff authorization';
  END IF;
  IF has_table_privilege('testagram_app','testagram_auth.staff_role_grants','INSERT')
     OR has_table_privilege('testagram_app','testagram_auth.staff_role_grants','UPDATE')
     OR has_table_privilege('testagram_app','testagram_auth.staff_role_grants','DELETE')
     OR has_table_privilege('testagram_app','testagram_auth.staff_principals','INSERT')
     OR has_table_privilege('testagram_app','testagram_auth.staff_principals','UPDATE')
     OR has_table_privilege('testagram_app','testagram_auth.staff_principals','DELETE') THEN
    RAISE EXCEPTION 'Backend runtime role must not mutate native staff authorization';
  END IF;
END;
$native_auth_schema$;
SQL

# Exercise the complete PostgreSQL native identity provisioning transaction.
TEST_DATABASE_URL='postgres://postgres:ci-only-password@localhost:5432/postgres?sslmode=disable' go test ./cmd/api -run '^(TestMailOutboxPersistsEncryptedPayload|TestSocialMailOutboxCarriesConsentCategory|TestMailOutboxInsertRollsBackWithCallerTransaction|TestNativeSignupQueuesEmailOTPAndOTPVerifiesAccount|TestSocialMailConsentIsRecheckedAgainstCurrentPreference|TestSocialFollowAndPostEmailsOnlyQueueForConsentingFollowers|TestNativeIdentityProvisioningTokenAndRedeemIntegration|TestNativeIdentityApplicationHTTPJourneyIntegration|TestNativeIdentityEvidenceHTTPUploadSubmitAndReplayIntegration|TestNativeApprovedIdentityProvisioningAndEmailVerification|TestNativeIdentityApplicationStatusUsesVerificationSessions|TestIdentityReviewerHonorsNativeRoleRevocationAndMFA)$' -count=1

# Consent evidence must be immutable after insertion.
if psql_ci -c "UPDATE testagram_identity.legal_consent_audit SET request_id='tampered' WHERE request_id='ci-registration-request';" >/dev/null 2>&1; then
  echo "legal consent audit mutation unexpectedly succeeded" >&2
  exit 1
fi

# Seed one legacy saga state so the archive migration must transform the
# provider-specific status value, not merely prove the empty-table case.
psql_ci <<'SQL'
DO $seed_historical_status$
DECLARE
  affected BIGINT;
BEGIN
  UPDATE testagram_identity.registration_provisioning
  SET status='appwrite_created'
  WHERE application_id=(
    SELECT id FROM testagram_identity.identity_applications
    WHERE email='first@example.test'
  );
  GET DIAGNOSTICS affected = ROW_COUNT;
  IF affected <> 1 THEN
    RAISE EXCEPTION 'expected exactly one historical provisioning fixture, got %', affected;
  END IF;
END;
$seed_historical_status$;
SQL

# Reproduce production's dual-ledger state: an existing archive plus a
# second legacy-named provisioning table. Both ledgers must remain restricted.
psql_ci <<'SQL'
CREATE TABLE testagram_identity.historical_external_registration_provisioning (
  id BIGINT PRIMARY KEY,
  source_account_id TEXT,
  canonical_user_id UUID,
  status TEXT NOT NULL CHECK (status IN (
    'pending','source_provider_created','linked','verification_sent',
    'completed','compensation_required','failed'
  )),
  CONSTRAINT registration_provisioning_source_user_id_key UNIQUE(source_account_id)
);
INSERT INTO testagram_identity.historical_external_registration_provisioning
  (id,source_account_id,status)
VALUES (9001,'already-archived-source-account','source_provider_created');
SQL

# Simulate the exact partially-applied production state from the first deploy:
# the legacy table and source-account column were renamed before the migration
# stopped while renaming its unique constraint/index. Re-running the fixed
# migration must finish safely from this state.
psql_ci <<'SQL'
ALTER TABLE testagram_identity.registration_provisioning
  RENAME TO historical_external_registration_provisioning_recovered;
ALTER TABLE testagram_identity.historical_external_registration_provisioning_recovered
  RENAME COLUMN appwrite_user_id TO source_account_id;
SQL

# Apply the forward-only retirement migrations after all legacy compatibility
# fixtures have been exercised. This proves the live schema no longer exposes
# provider-keyed tables/columns while preserving restricted historical evidence.
psql_ci < migrations/20261120_native_identity_historical_account_archive.sql
psql_ci < migrations/20261120_native_identity_historical_staff_archive.sql
psql_ci < migrations/20261120_native_identity_historical_admin_archive.sql
psql_ci < migrations/20261120_native_identity_historical_attribution_archive.sql

# The production migration runner reapplies its ordered manifest on every run.
# Reapply the archive sequence to prove every rename/revoke is safe to repeat.
psql_ci < migrations/20261120_native_identity_historical_account_archive.sql
psql_ci < migrations/20261120_native_identity_historical_staff_archive.sql
psql_ci < migrations/20261120_native_identity_historical_admin_archive.sql
psql_ci < migrations/20261120_native_identity_historical_attribution_archive.sql

# Match the complete production runner manifest after provider-keyed
# tables/columns have already been archived. Replaying every migration here
# prevents a later legacy DDL statement from undoing the native cutover.
for migration in \
  migrations/20261107_federation_actor_consent.sql \
  migrations/20261111_admin_security_controls.sql \
  migrations/20261111_identity_registration_pipeline.sql \
  migrations/20261113_identity_registration_hardening.sql \
  migrations/20261113_native_identity_verification.sql \
  migrations/20261114_native_identity_evidence.sql \
  migrations/20261114_native_identity_integrity.sql \
  migrations/20261115_identity_evidence_access_audit.sql \
  migrations/20261115_native_identity_status_access.sql \
  migrations/20261115_testagram_native_auth_cutover_foundation.sql \
  migrations/20261116_native_approved_identity_provisioning.sql \
  migrations/20261116_native_auth_runtime_grants.sql \
  migrations/20261117_native_staff_reviewer_grants.sql \
  migrations/20261118_native_admin_console.sql \
  migrations/20261119_native_identity_approval_ledger.sql \
  migrations/20261120_native_verification_decider.sql \
  migrations/20261122_testagram_mail_outbox.sql \
  migrations/20261123_testagram_mail_outbox_runtime_role.sql \
  migrations/20261120_native_identity_historical_account_archive.sql \
  migrations/20261120_native_identity_historical_staff_archive.sql \
  migrations/20261120_native_identity_historical_admin_archive.sql \
  migrations/20261120_native_identity_historical_attribution_archive.sql \
  migrations/20261120_native_identity_approval_circuit.sql \
  migrations/20261121_native_identity_intake_optional_fingerprint.sql \
  migrations/20261121_native_staff_webauthn.sql \
  migrations/061_native_sensitive_action_integrity.sql \
  migrations/20261124_testagram_email_verification_otp.sql \
  migrations/20261124_testagram_social_email_consent.sql \
  migrations/20261125_testagram_mail_delivery_events.sql \
  migrations/20261126_testagram_native_mail_feedback.sql; do
  psql_ci < "$migration"
done

psql_ci <<'SQL'
DO $retirement_acceptance$
BEGIN
  IF to_regclass('testagram_identity.appwrite_accounts') IS NOT NULL
     OR to_regclass('testagram_identity.link_audit') IS NOT NULL
     OR to_regclass('testagram_identity.appwrite_registration_consents') IS NOT NULL
     OR to_regclass('testagram_identity.registration_provisioning') IS NOT NULL
     OR to_regclass('testagram_auth.staff_roles') IS NOT NULL
     OR to_regclass('testagram_auth.staff_role_audit') IS NOT NULL
     OR to_regclass('testagram_auth.staff_permission_overrides') IS NOT NULL
     OR to_regclass('testagram_auth.sensitive_action_requests') IS NOT NULL
     OR to_regclass('testagram_auth.sensitive_action_approvals') IS NOT NULL
     OR to_regclass('testagram_auth.admin_security_audit') IS NOT NULL
     OR to_regclass('testagram_identity.identity_applications_sensitive_action_request_idx') IS NOT NULL
     OR to_regclass('testagram_identity.verification_reviews_reviewer_idx') IS NOT NULL THEN
    RAISE EXCEPTION 'provider-keyed identity or staff tables remain in the live schema';
  END IF;

  IF to_regclass('testagram_identity.historical_external_identity_accounts') IS NULL
     OR to_regclass('testagram_identity.historical_external_identity_link_audit') IS NULL
     OR to_regclass('testagram_identity.historical_external_registration_consents') IS NULL
     OR to_regclass('testagram_identity.historical_external_registration_provisioning') IS NULL
      OR to_regclass('testagram_identity.historical_external_registration_provisioning_recovered') IS NULL
     OR to_regclass('testagram_auth.historical_external_staff_roles') IS NULL
     OR to_regclass('testagram_auth.historical_external_staff_role_audit') IS NULL
     OR to_regclass('testagram_auth.historical_external_sensitive_action_requests') IS NULL
     OR to_regclass('testagram_auth.historical_external_sensitive_action_approvals') IS NULL
     OR to_regclass('testagram_auth.historical_external_admin_security_audit') IS NULL
     OR to_regclass('testagram_identity.native_registration_provisioning') IS NULL
     OR to_regclass('testagram_auth.native_sensitive_action_requests') IS NULL
     OR to_regclass('testagram_auth.native_sensitive_action_approvals') IS NULL
     OR to_regclass('testagram_identity.historical_external_identity_applications_sensitive_request_idx') IS NULL
     OR to_regclass('testagram_identity.historical_source_verification_reviews_reviewer_idx') IS NULL THEN
    RAISE EXCEPTION 'historical evidence or native replacement tables are missing';
  END IF;

  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='testagram_identity'
      AND ((table_name='identity_application_reviews' AND column_name='reviewer_appwrite_user_id')
        OR (table_name='verification_reviews' AND column_name='reviewer_appwrite_user_id')
        OR (table_name='verification_sessions' AND column_name='decided_by_appwrite_user_id')
        OR (table_name='identity_approval_tokens' AND column_name IN (
          'first_approver_appwrite_user_id','second_approver_appwrite_user_id',
          'consumed_by_appwrite_user_id'))
        OR (table_name='identity_applications' AND column_name='sensitive_action_request_id'))
  ) THEN
    RAISE EXCEPTION 'provider-keyed reviewer/approver columns remain on live identity tables';
  END IF;

  IF EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid IN (
      'testagram_identity.historical_external_identity_accounts'::regclass,
      'testagram_identity.historical_external_identity_link_audit'::regclass,
      'testagram_identity.historical_external_registration_consents'::regclass,
      'testagram_identity.historical_external_registration_provisioning'::regclass,
      'testagram_identity.historical_external_registration_provisioning_recovered'::regclass,
      'testagram_auth.historical_external_staff_roles'::regclass,
      'testagram_auth.historical_external_staff_role_audit'::regclass,
      'testagram_auth.historical_external_staff_permission_overrides'::regclass,
      'testagram_auth.historical_external_sensitive_action_requests'::regclass,
      'testagram_auth.historical_external_sensitive_action_approvals'::regclass,
      'testagram_auth.historical_external_admin_security_audit'::regclass
    ) AND conname ILIKE '%appwrite%'
  ) THEN
    RAISE EXCEPTION 'provider-branded constraints remain on archived relations';
  END IF;

  IF EXISTS (
    SELECT 1
    FROM pg_index i JOIN pg_class idx ON idx.oid=i.indexrelid
    WHERE i.indrelid IN (
      'testagram_identity.historical_external_identity_accounts'::regclass,
      'testagram_identity.historical_external_identity_link_audit'::regclass,
      'testagram_identity.historical_external_registration_consents'::regclass,
      'testagram_identity.historical_external_registration_provisioning'::regclass,
      'testagram_identity.historical_external_registration_provisioning_recovered'::regclass,
      'testagram_auth.historical_external_staff_roles'::regclass,
      'testagram_auth.historical_external_staff_role_audit'::regclass,
      'testagram_auth.historical_external_staff_permission_overrides'::regclass,
      'testagram_auth.historical_external_sensitive_action_requests'::regclass,
      'testagram_auth.historical_external_sensitive_action_approvals'::regclass,
      'testagram_auth.historical_external_admin_security_audit'::regclass
    ) AND idx.relname ILIKE '%appwrite%'
  ) THEN
    RAISE EXCEPTION 'provider-branded indexes remain on archived relations';
  END IF;

  IF EXISTS (
    SELECT 1 FROM testagram_identity.historical_external_registration_provisioning
    WHERE status='appwrite_created'
  ) OR EXISTS (
    SELECT 1 FROM testagram_identity.historical_external_registration_provisioning_recovered
    WHERE status='appwrite_created'
  ) THEN
    RAISE EXCEPTION 'provider-specific provisioning status remains in historical ledger';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM testagram_identity.historical_external_registration_provisioning
    WHERE status='source_provider_created'
  ) OR NOT EXISTS (
    SELECT 1 FROM testagram_identity.historical_external_registration_provisioning_recovered
    WHERE status='source_provider_created'
  ) THEN
    RAISE EXCEPTION 'both historical provider-created provisioning events were not preserved under neutral statuses';
  END IF;
  IF (SELECT count(*) FROM testagram_identity.historical_external_registration_provisioning) <> 1
     OR (SELECT count(*) FROM testagram_identity.historical_external_registration_provisioning_recovered) <> 1 THEN
    RAISE EXCEPTION 'both historical provisioning ledgers were not retained independently';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='historical_external_registration_provisioning_recovered' AND column_name='source_account_id')
     OR EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='historical_external_registration_provisioning_recovered' AND column_name='appwrite_user_id') THEN
    RAISE EXCEPTION 'recovered provisioning source identifier was not neutralized';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid='testagram_identity.historical_external_registration_provisioning'::regclass
      AND contype='u'
      AND conname='registration_provisioning_source_user_id_key'
  ) OR NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid='testagram_identity.historical_external_registration_provisioning_recovered'::regclass
      AND contype='u'
      AND conname LIKE 'registration_provisioning_source_user_id_key_recovered_%'
      AND octet_length(conname) <= 63
  ) THEN
    RAISE EXCEPTION 'canonical and recovered provisioning ledgers did not retain distinct unique constraints';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='testagram_identity' AND table_name='identity_applications'
      AND column_name='historical_external_sensitive_action_request_id'
  ) OR NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='testagram_identity' AND table_name='identity_applications'
      AND column_name='native_sensitive_action_request_id'
  ) OR NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid='testagram_identity.identity_applications'::regclass
      AND conname='identity_applications_historical_request_fk'
  ) THEN
    RAISE EXCEPTION 'historical and native identity approval request links are not separated';
  END IF;

  IF (
    SELECT count(*) FROM pg_constraint
    WHERE confrelid='testagram_auth.historical_external_staff_roles'::regclass
      AND conname IN (
        'identity_application_reviews_historical_source_reviewer_fk',
        'identity_approval_tokens_historical_first_approver_fk',
        'identity_approval_tokens_historical_second_approver_fk',
        'verification_reviews_historical_source_reviewer_fk',
        'verification_sessions_historical_source_decider_fk'
      )
  ) <> 5 THEN
    RAISE EXCEPTION 'historical staff foreign-key audit lineage was not preserved';
  END IF;

  IF NOT has_table_privilege('testagram_app','testagram_identity.native_registration_provisioning','SELECT')
     OR NOT has_table_privilege('testagram_app','testagram_identity.native_registration_provisioning','INSERT')
     OR NOT has_table_privilege('testagram_app','testagram_identity.native_registration_provisioning','UPDATE')
     OR NOT has_table_privilege('testagram_app','testagram_auth.native_sensitive_action_requests','SELECT')
     OR NOT has_table_privilege('testagram_app','testagram_auth.native_sensitive_action_approvals','SELECT') THEN
    RAISE EXCEPTION 'runtime role lost required native identity or approval access';
  END IF;

  IF has_table_privilege('testagram_app','testagram_identity.historical_external_identity_accounts','SELECT')
     OR has_table_privilege('testagram_app','testagram_identity.historical_external_registration_provisioning','SELECT')
     OR has_table_privilege('testagram_app','testagram_identity.historical_external_registration_provisioning_recovered','SELECT')
      OR has_table_privilege('testagram_app','testagram_auth.historical_external_staff_roles','SELECT')
     OR has_table_privilege('testagram_app','testagram_auth.historical_external_sensitive_action_requests','SELECT') THEN
    RAISE EXCEPTION 'runtime role can still read archived historical identity or staff data';
  END IF;
END;
$retirement_acceptance$;
SQL

# Fresh-install fixture: the production Compose manifest intentionally does
# not mount the retired account-link/audit/consent migrations. The archive must
# therefore succeed when those optional historical tables never existed.
psql_ci -c "CREATE DATABASE native_clean_archive_test;"
psql_clean <<'SQL'
CREATE SCHEMA testagram_identity;
CREATE SCHEMA testagram_auth;
CREATE TABLE testagram_identity.registration_provisioning (
  id BIGINT PRIMARY KEY,
  appwrite_user_id TEXT,
  canonical_user_id UUID,
  status TEXT NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending','appwrite_created','linked','verification_sent','completed','compensation_required','failed')),
  CHECK ((status IN ('linked','verification_sent','completed')) =
    (appwrite_user_id IS NOT NULL AND canonical_user_id IS NOT NULL))
);
INSERT INTO testagram_identity.registration_provisioning(id,status)
VALUES (1,'appwrite_created');
CREATE TABLE testagram_auth.legacy_account_reconciliation (
  source_provider TEXT NOT NULL
    CONSTRAINT legacy_account_reconciliation_source_provider_check
    CHECK (source_provider='appwrite')
);
INSERT INTO testagram_auth.legacy_account_reconciliation(source_provider)
VALUES ('appwrite');
SQL
psql_clean < migrations/20261120_native_identity_historical_account_archive.sql
psql_clean < migrations/20261120_native_identity_historical_account_archive.sql
psql_clean <<'SQL'
DO $clean_install_archive$
BEGIN
  IF to_regclass('testagram_identity.appwrite_accounts') IS NOT NULL
     OR to_regclass('testagram_identity.link_audit') IS NOT NULL
     OR to_regclass('testagram_identity.appwrite_registration_consents') IS NOT NULL THEN
    RAISE EXCEPTION 'clean install unexpectedly created optional historical provider tables';
  END IF;
  IF to_regclass('testagram_identity.historical_external_identity_accounts') IS NOT NULL
     OR to_regclass('testagram_identity.historical_external_identity_link_audit') IS NOT NULL
     OR to_regclass('testagram_identity.historical_external_registration_consents') IS NOT NULL THEN
    RAISE EXCEPTION 'clean install created historical tables without source data';
  END IF;
  IF to_regclass('testagram_identity.historical_external_registration_provisioning') IS NULL THEN
    RAISE EXCEPTION 'clean install did not archive the legacy provisioning ledger';
  END IF;
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='testagram_identity'
      AND table_name='historical_external_registration_provisioning'
      AND column_name='appwrite_user_id'
  ) OR NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='testagram_identity'
      AND table_name='historical_external_registration_provisioning'
      AND column_name='source_account_id'
  ) THEN
    RAISE EXCEPTION 'clean-install historical provisioning column was not neutralized';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM testagram_identity.historical_external_registration_provisioning
    WHERE status='source_provider_created'
  ) THEN
    RAISE EXCEPTION 'clean-install provisioning state was not preserved';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM testagram_auth.legacy_account_reconciliation
    WHERE source_provider='historical_external_provider'
  ) THEN
    RAISE EXCEPTION 'clean-install reconciliation provenance was not normalized';
  END IF;
END;
$clean_install_archive$;
SQL

echo "Legacy-provider retirement, native replacement, historical retention and runtime privilege checks passed."

# Reproduce the production ordering defect: remove the NOLOGIN application
# group role after all other identity tests, then prove the corrective migration
# recreates it and restores the exact outbox grants on an existing database.
psql_ci <<'SQL'
REASSIGN OWNED BY testagram_app TO postgres;
DROP OWNED BY testagram_app;
DROP ROLE testagram_app;
SQL
psql_ci < migrations/20261123_testagram_mail_outbox_runtime_role.sql
psql_ci < migrations/20261123_testagram_mail_outbox_runtime_role.sql
psql_ci < migrations/20261124_testagram_social_email_consent.sql
psql_ci < migrations/20261125_testagram_mail_delivery_events.sql
psql_ci < migrations/20261126_testagram_native_mail_feedback.sql
psql_ci <<'SQL'
DO $mail_role_repair$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    RAISE EXCEPTION 'corrective migration did not recreate testagram_app';
  END IF;
  IF NOT has_schema_privilege('testagram_app','testagram_mail','USAGE')
     OR NOT has_table_privilege('testagram_app','testagram_mail.outbox','SELECT')
     OR NOT has_table_privilege('testagram_app','testagram_mail.outbox','INSERT')
     OR NOT has_table_privilege('testagram_app','testagram_mail.outbox','UPDATE')
     OR NOT has_sequence_privilege('testagram_app','testagram_mail.outbox_id_seq','USAGE')
     OR NOT has_sequence_privilege('testagram_app','testagram_mail.outbox_id_seq','SELECT') THEN
    RAISE EXCEPTION 'corrective migration did not restore the exact mail outbox grants';
  END IF;

  IF to_regclass('testagram_mail.return_path_tokens') IS NULL
     OR to_regclass('testagram_mail.inbound_reports') IS NULL THEN
    RAISE EXCEPTION 'native mail feedback migration did not create its required ledgers';
  END IF;
  IF NOT has_table_privilege('testagram_app','testagram_mail.return_path_tokens','SELECT')
     OR NOT has_table_privilege('testagram_app','testagram_mail.return_path_tokens','INSERT')
     OR NOT has_table_privilege('testagram_app','testagram_mail.return_path_tokens','UPDATE')
     OR NOT has_table_privilege('testagram_app','testagram_mail.return_path_tokens','DELETE')
     OR NOT has_table_privilege('testagram_app','testagram_mail.inbound_reports','SELECT')
     OR NOT has_table_privilege('testagram_app','testagram_mail.inbound_reports','INSERT')
     OR NOT has_table_privilege('testagram_app','testagram_mail.inbound_reports','DELETE')
     OR NOT has_sequence_privilege('testagram_app','testagram_mail.inbound_reports_id_seq','USAGE')
     OR NOT has_sequence_privilege('testagram_app','testagram_mail.inbound_reports_id_seq','SELECT') THEN
    RAISE EXCEPTION 'native mail feedback runtime grants are incomplete';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid='testagram_mail.delivery_events'::regclass
      AND conname='testagram_mail_delivery_events_event_type_check'
      AND pg_get_constraintdef(oid) LIKE '%dsn.delivered%'
  ) THEN
    RAISE EXCEPTION 'native mail delivery event types were not enabled';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='testagram_mail' AND table_name='delivery_events'
      AND column_name='transport'
  ) OR NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='testagram_mail' AND table_name='delivery_events'
      AND column_name='report_sha256'
  ) THEN
    RAISE EXCEPTION 'provider-neutral native mail event metadata is missing';
  END IF;
END;
$mail_role_repair$;
SQL

# Regression: upgrade a database where the historical staff-role table is absent.
# The old admin migration must preserve shared catalog tables without recreating
# obsolete staff authorization, and the optional archive migrations must be no-ops.
psql_ci -c "CREATE DATABASE native_partial_legacy_test;"
psql_partial() {
  docker exec -i "$container" psql -U postgres -d native_partial_legacy_test -v ON_ERROR_STOP=1 "$@"
}
psql_partial <<'SQL'
CREATE SCHEMA testagram_auth;
SQL
psql_partial < migrations/20261111_admin_security_controls.sql
psql_partial < migrations/20261120_native_identity_historical_staff_archive.sql
psql_partial < migrations/20261120_native_identity_historical_admin_archive.sql
psql_partial <<'SQL'
DO $partial_legacy_acceptance$
BEGIN
  IF to_regclass('testagram_auth.staff_roles') IS NOT NULL
     OR to_regclass('testagram_auth.historical_external_staff_roles') IS NOT NULL
     OR to_regclass('testagram_auth.historical_external_staff_role_audit') IS NOT NULL
     OR to_regclass('testagram_auth.staff_permission_overrides') IS NOT NULL
     OR to_regclass('testagram_auth.sensitive_action_requests') IS NOT NULL
     OR to_regclass('testagram_auth.sensitive_action_approvals') IS NOT NULL
     OR to_regclass('testagram_auth.admin_security_audit') IS NOT NULL
     OR to_regclass('testagram_auth.historical_external_staff_permission_overrides') IS NOT NULL
     OR to_regclass('testagram_auth.historical_external_sensitive_action_requests') IS NOT NULL
     OR to_regclass('testagram_auth.historical_external_sensitive_action_approvals') IS NOT NULL
     OR to_regclass('testagram_auth.historical_external_admin_security_audit') IS NOT NULL THEN
    RAISE EXCEPTION 'partial legacy upgrade unexpectedly created obsolete staff authorization state';
  END IF;
  IF to_regclass('testagram_auth.admin_permissions') IS NULL
     OR to_regclass('testagram_auth.admin_rate_limits') IS NULL THEN
    RAISE EXCEPTION 'partial legacy upgrade omitted required admin catalog tables';
  END IF;
END;
$partial_legacy_acceptance$;
SQL

# Regression: a legacy table name may survive while its expected columns do not.
# The migration must archive the table without attempting legacy FK/column DDL.
psql_ci -c "CREATE DATABASE native_partial_staff_table_test;"
psql_partial_staff() {
  docker exec -i "$container" psql -U postgres -d native_partial_staff_table_test -v ON_ERROR_STOP=1 "$@"
}
psql_partial_staff <<'SQL'
CREATE SCHEMA testagram_auth;
CREATE TABLE testagram_auth.staff_roles (legacy_marker text);
SQL
psql_partial_staff < migrations/20261111_admin_security_controls.sql
psql_partial_staff < migrations/20261120_native_identity_historical_staff_archive.sql
psql_partial_staff < migrations/20261120_native_identity_historical_admin_archive.sql
psql_partial_staff <<'SQL'
DO $partial_staff_table_acceptance$
BEGIN
  IF to_regclass('testagram_auth.staff_roles') IS NOT NULL
     OR to_regclass('testagram_auth.historical_external_staff_roles') IS NULL THEN
    RAISE EXCEPTION 'incomplete legacy staff table was not safely archived';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='testagram_auth'
      AND table_name='historical_external_staff_roles'
      AND column_name='legacy_marker'
  ) THEN
    RAISE EXCEPTION 'incomplete legacy staff table data shape was not preserved';
  END IF;
END;
$partial_staff_table_acceptance$;
SQL

echo "Native identity migrations, evidence-access audit and registration invariants passed."

# Exercise Cloudflare lifecycle reconciliation, duplicate-event protection, complaint suppression,
# and RFC one-click social unsubscribe against the isolated PostgreSQL schema.
TEST_DATABASE_URL='postgres://postgres:ci-only-password@localhost:5432/postgres?sslmode=disable' TESTAGRAM_MAIL_ENCRYPTION_KEY='0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef' go test ./cmd/api -run '^(TestCloudflareDeliveryEventIsIdempotentAndComplaintSuppressesRecipient|TestSocialEmailUnsubscribeGetIsSafeAndPostWithdrawsConsent|TestMailWorkerRetriesDefiniteFailureAndPreservesPayload|TestMailWorkerQuarantinesAmbiguousOutcomeWithoutResending|TestMailWorkerNeverSendsPendingSocialMailAfterConsentWithdrawal|TestNativeBounceReportCorrelatesAndDeduplicatesHardBounce)$' -count=1

# Exercise native password recovery and the complete refresh/logout lifecycle against
# the same isolated PostgreSQL schema used by the registration acceptance tests.

TEST_DATABASE_URL='postgres://postgres:ci-only-password@localhost:5432/postgres?sslmode=disable' TESTAGRAM_MAIL_ENCRYPTION_KEY='0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef' go test ./cmd/api -run '^(TestNativePasswordRecoveryRequestIsAntiEnumeration|TestNativePasswordResetConsumesTokenRevokesSessionsAndAllowsNewLogin|TestNativeSessionRefreshRotationAndReuseRevokesFamily|TestNativeSessionLogoutRevokesCurrentSession|TestNativeSessionLogoutAllRevokesEverySession|TestNativeSessionConcurrentRefreshReuseRevokesFamily)$' -count=1


# Exercise the Admin Console review handler against this same isolated database.
TEST_DATABASE_URL='postgres://postgres:ci-only-password@localhost:5432/postgres?sslmode=disable' go test ./admin-console -run '^TestRecordIdentityApplicationReviewIntegration$' -count=1

# Verify Backend and Admin Console readiness use the same disjoint PostgreSQL staff quorum.
TEST_DATABASE_URL='postgres://postgres:ci-only-password@localhost:5432/postgres?sslmode=disable' go test ./cmd/api -run '^TestIdentityReviewWebAuthnReadinessRequiresIndependentCredentialedStaff$' -count=1

# Verify the Admin Console's signed MFA elevation is accepted by Backend staff authorization
# and that revoking the underlying PostgreSQL session invalidates that elevation.
TEST_DATABASE_URL='postgres://postgres:ci-only-password@localhost:5432/postgres?sslmode=disable' go test ./cmd/api -run '^TestAdminConsoleElevationTokenAcceptedByNativeIdentityReviewer$' -count=1

# Recovery must wait for the native auth/profile schema, not just PostgreSQL Ping.
TEST_DATABASE_URL='postgres://postgres:ci-only-password@localhost:5432/postgres?sslmode=disable' go test ./cmd/api -run '^TestEdgeRecoveryReadinessRequiresNativeAuthPostgresSchema$' -count=1

# Consent evidence must be immutable after insertion.
if psql_ci -c "UPDATE testagram_identity.legal_consent_audit SET request_id='tampered' WHERE request_id='ci-registration-request';" >/dev/null 2>&1; then
  echo "legal consent audit mutation unexpectedly succeeded" >&2
  exit 1
fi

# Reproduce the production ordering defect: remove the NOLOGIN application
# group role after all other identity tests, then prove the corrective migration
# recreates it and restores the exact outbox grants on an existing database.
psql_ci <<'SQL'
REASSIGN OWNED BY testagram_app TO postgres;
DROP OWNED BY testagram_app;
DROP ROLE testagram_app;
SQL
psql_ci < migrations/20261123_testagram_mail_outbox_runtime_role.sql
psql_ci < migrations/20261123_testagram_mail_outbox_runtime_role.sql
psql_ci < migrations/20261124_testagram_social_email_consent.sql
psql_ci < migrations/20261125_testagram_mail_delivery_events.sql
psql_ci < migrations/20261126_testagram_native_mail_feedback.sql
psql_ci <<'SQL'
DO $mail_role_repair$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    RAISE EXCEPTION 'corrective migration did not recreate testagram_app';
  END IF;
  IF NOT has_schema_privilege('testagram_app','testagram_mail','USAGE')
     OR NOT has_table_privilege('testagram_app','testagram_mail.outbox','SELECT')
     OR NOT has_table_privilege('testagram_app','testagram_mail.outbox','INSERT')
     OR NOT has_table_privilege('testagram_app','testagram_mail.outbox','UPDATE')
     OR NOT has_sequence_privilege('testagram_app','testagram_mail.outbox_id_seq','USAGE')
     OR NOT has_sequence_privilege('testagram_app','testagram_mail.outbox_id_seq','SELECT') THEN
    RAISE EXCEPTION 'corrective migration did not restore the exact mail outbox grants';
  END IF;
END;
$mail_role_repair$;
SQL

echo "Native identity migrations, evidence-access audit and registration invariants passed."
