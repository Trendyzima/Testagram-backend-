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

psql_ci <<'SQL'
CREATE ROLE testagram_app NOLOGIN;
CREATE ROLE service_role NOLOGIN;
SQL

for migration in \
  migrations/20261019_testagram_independent_auth.sql \
  migrations/20261106_appwrite_identity_bridge.sql \
  migrations/20261110_staff_roles.sql \
  migrations/20261111_admin_security_controls.sql \
  migrations/20261111_identity_registration_pipeline.sql \
  migrations/20261113_identity_registration_hardening.sql; do
  psql_ci < "$migration"
done

# Re-apply the new migration to prove idempotent DDL behavior.
psql_ci < migrations/20261113_identity_registration_hardening.sql

psql_ci <<'SQL'
INSERT INTO testagram_auth.staff_roles(appwrite_user_id,role)
VALUES ('ci-reviewer-one','administrator'),('ci-reviewer-two','administrator'),('ci-intake-service','operations');

DO $registration$
DECLARE
  app_id uuid := gen_random_uuid();
  action_id uuid := gen_random_uuid();
  provisioning_id uuid;
  premature_token_rejected boolean := FALSE;
BEGIN
  INSERT INTO testagram_auth.sensitive_action_requests
    (id,action_type,requested_by_appwrite_user_id,target_reference,reason,payload,payload_sha256)
  VALUES
    (action_id,'identity.approve','ci-intake-service',app_id::text,
     'CI registration approval request',jsonb_build_object('application_id',app_id::text,'email','first@example.test',
       'username','ci_first','display_name','CI First',
       'evidence_fingerprint_sha256',repeat('1',64),
       'terms_version','terms-v1','privacy_version','privacy-v1'),repeat('a',64));

  INSERT INTO testagram_identity.identity_applications
    (id,email,username,display_name,adult_confirmed_at,adult_verified_at,
     adult_verification_provider,adult_verification_reference,adult_verification_checked_at,
     evidence_reference,evidence_fingerprint_sha256,terms_version,privacy_version,
     terms_accepted_at,privacy_accepted_at,submission_key_sha256,sensitive_action_request_id)
  VALUES
    (app_id,'first@example.test','ci_first','CI First',now(),now(),
     'ci-provider','ci-adult-evidence-ref',now(),
     'opaque-ci-evidence-ref',decode(repeat('1',64),'hex'),'terms-v1','privacy-v1',now(),now(),
     decode(repeat('2',64),'hex'),action_id);

  -- A token cannot be issued while the ledger is still pending.
  BEGIN
    BEGIN
      INSERT INTO testagram_identity.identity_approval_tokens
        (token_sha256,jti_sha256,application_id,first_approver_appwrite_user_id,
         second_approver_appwrite_user_id,expires_at)
      VALUES
        (decode(repeat('a',64),'hex'),decode(repeat('b',64),'hex'),app_id,
         'ci-reviewer-one','ci-reviewer-two',now()+interval '10 minutes');
    EXCEPTION WHEN raise_exception THEN
      premature_token_rejected := TRUE;
    END;
    IF NOT premature_token_rejected THEN
      RAISE EXCEPTION 'token issuance before two-person approval was accepted';
    END IF;
  END;

  INSERT INTO testagram_identity.identity_application_reviews
    (application_id,reviewer_appwrite_user_id,decision,reason,evidence_audit_reference)
  VALUES
    (app_id,'ci-reviewer-one','approved','First independent approval','audit-reference-0001'),
    (app_id,'ci-reviewer-two','approved','Second independent approval','audit-reference-0002');

  INSERT INTO testagram_auth.sensitive_action_approvals
    (request_id,approver_appwrite_user_id,decision,reason)
  VALUES
    (action_id,'ci-reviewer-one','approved','First independent approval'),
    (action_id,'ci-reviewer-two','approved','Second independent approval');

  UPDATE testagram_auth.sensitive_action_requests
  SET status='approved',decided_at=now()
  WHERE id=action_id;
  UPDATE testagram_identity.identity_applications
  SET status='approved'
  WHERE id=app_id;

  INSERT INTO testagram_identity.identity_approval_tokens
    (token_sha256,jti_sha256,application_id,first_approver_appwrite_user_id,
     second_approver_appwrite_user_id,expires_at)
  VALUES
    (decode(repeat('3',64),'hex'),decode(repeat('4',64),'hex'),app_id,
     'ci-reviewer-one','ci-reviewer-two',now()+interval '10 minutes');

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
    WHERE id=app_id AND sensitive_action_request_id=action_id
  ) THEN
    RAISE EXCEPTION 'application-to-approval-ledger link missing';
  END IF;
  IF (SELECT count(*) FROM testagram_identity.identity_application_reviews WHERE application_id=app_id AND decision='approved') <> 2 THEN
    RAISE EXCEPTION 'two independent reviewer records were not persisted';
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

# Consent evidence must be immutable after insertion.
if psql_ci -c "UPDATE testagram_identity.legal_consent_audit SET request_id='tampered' WHERE request_id='ci-registration-request';" >/dev/null 2>&1; then
  echo "legal consent audit mutation unexpectedly succeeded" >&2
  exit 1
fi

echo "Identity registration hardening migration and invariants passed."
