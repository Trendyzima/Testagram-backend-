-- Registration pipeline hardening.
-- This migration only strengthens durable invariants. It does NOT enable public
-- registration, create Appwrite users, or grant reviewer permissions.
BEGIN;

-- One idempotency key per submitted application. The API must hash the client
-- key with SHA-256 and never persist the raw key.
ALTER TABLE testagram_identity.identity_applications
  ADD COLUMN IF NOT EXISTS submission_key_sha256 BYTEA,
  ADD COLUMN IF NOT EXISTS sensitive_action_request_id UUID,
  ADD COLUMN IF NOT EXISTS adult_verification_provider TEXT,
  ADD COLUMN IF NOT EXISTS adult_verification_reference TEXT,
  ADD COLUMN IF NOT EXISTS adult_verification_checked_at TIMESTAMPTZ;

DO $preflight$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM testagram_identity.identity_applications
    WHERE status NOT IN ('rejected','expired','cancelled')
    GROUP BY email
    HAVING count(*) > 1
  ) THEN
    RAISE EXCEPTION 'identity registration hardening: duplicate active application emails must be reconciled before migration';
  END IF;

  IF EXISTS (
    SELECT 1
    FROM testagram_identity.identity_applications
    WHERE status NOT IN ('rejected','expired','cancelled')
    GROUP BY lower(username)
    HAVING count(*) > 1
  ) THEN
    RAISE EXCEPTION 'identity registration hardening: duplicate active application usernames must be reconciled before migration';
  END IF;
END;
$preflight$;

CREATE UNIQUE INDEX IF NOT EXISTS identity_applications_active_email_ci_idx
  ON testagram_identity.identity_applications(lower(email))
  WHERE status NOT IN ('rejected','expired','cancelled');

CREATE UNIQUE INDEX IF NOT EXISTS identity_applications_active_username_ci_idx
  ON testagram_identity.identity_applications(lower(username))
  WHERE status NOT IN ('rejected','expired','cancelled');

CREATE UNIQUE INDEX IF NOT EXISTS identity_applications_submission_key_idx
  ON testagram_identity.identity_applications(submission_key_sha256)
  WHERE submission_key_sha256 IS NOT NULL;

-- A self-attested age checkbox is not verified adulthood. Verified status must
-- be backed by a provider result and an opaque provider reference; no document
-- bytes, document number, or raw identity evidence is stored here.
DO $adult_check$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname = 'identity_applications_adult_verification_evidence_check'
      AND conrelid = 'testagram_identity.identity_applications'::regclass
  ) THEN
    ALTER TABLE testagram_identity.identity_applications
      ADD CONSTRAINT identity_applications_adult_verification_evidence_check
      CHECK (
        adult_verified_at IS NULL
        OR (
          adult_verification_provider IS NOT NULL
          AND length(btrim(adult_verification_provider)) BETWEEN 2 AND 64
          AND adult_verification_reference IS NOT NULL
          AND length(btrim(adult_verification_reference)) BETWEEN 8 AND 200
          AND adult_verification_checked_at IS NOT NULL
          AND adult_verification_checked_at <= adult_verified_at
        )
      ) NOT VALID;
  END IF;
END;
$adult_check$;

-- Link the registration application to the existing two-person approval
-- ledger. The application UUID is the target reference; the payload hash must
-- match the immutable application snapshot at approval time.
DO $approval_fk$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname = 'identity_applications_sensitive_action_request_fk'
      AND conrelid = 'testagram_identity.identity_applications'::regclass
  ) THEN
    ALTER TABLE testagram_identity.identity_applications
      ADD CONSTRAINT identity_applications_sensitive_action_request_fk
      FOREIGN KEY (sensitive_action_request_id)
      REFERENCES testagram_auth.sensitive_action_requests(id)
      ON DELETE RESTRICT NOT VALID;
  END IF;
END;
$approval_fk$;

CREATE UNIQUE INDEX IF NOT EXISTS identity_applications_sensitive_action_request_idx
  ON testagram_identity.identity_applications(sensitive_action_request_id)
  WHERE sensitive_action_request_id IS NOT NULL;

-- Enforce that new reviewer and approver identities correspond to provisioned
-- staff records. NOT VALID preserves legacy rows while enforcing new writes.
DO $reviewer_fks$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname = 'identity_application_reviews_reviewer_staff_fk'
      AND conrelid = 'testagram_identity.identity_application_reviews'::regclass
  ) THEN
    ALTER TABLE testagram_identity.identity_application_reviews
      ADD CONSTRAINT identity_application_reviews_reviewer_staff_fk
      FOREIGN KEY (reviewer_appwrite_user_id)
      REFERENCES testagram_auth.staff_roles(appwrite_user_id)
      ON DELETE RESTRICT NOT VALID;
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname = 'identity_approval_tokens_first_approver_staff_fk'
      AND conrelid = 'testagram_identity.identity_approval_tokens'::regclass
  ) THEN
    ALTER TABLE testagram_identity.identity_approval_tokens
      ADD CONSTRAINT identity_approval_tokens_first_approver_staff_fk
      FOREIGN KEY (first_approver_appwrite_user_id)
      REFERENCES testagram_auth.staff_roles(appwrite_user_id)
      ON DELETE RESTRICT NOT VALID;
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname = 'identity_approval_tokens_second_approver_staff_fk'
      AND conrelid = 'testagram_identity.identity_approval_tokens'::regclass
  ) THEN
    ALTER TABLE testagram_identity.identity_approval_tokens
      ADD CONSTRAINT identity_approval_tokens_second_approver_staff_fk
      FOREIGN KEY (second_approver_appwrite_user_id)
      REFERENCES testagram_auth.staff_roles(appwrite_user_id)
      ON DELETE RESTRICT NOT VALID;
  END IF;
END;
$reviewer_fks$;

-- The database itself refuses approval-token issuance unless the linked
-- sensitive-action request is approved by exactly two distinct, recorded
-- reviewers and the reviewed application snapshot matches the current row.
CREATE OR REPLACE FUNCTION testagram_identity.assert_two_person_identity_approval()
RETURNS trigger
LANGUAGE plpgsql
AS $function$
DECLARE
  v_request_id UUID;
  v_action_type TEXT;
  v_request_status TEXT;
  v_required_approvals SMALLINT;
  v_requester TEXT;
  v_payload JSONB;
  v_application RECORD;
  v_approved_count INTEGER;
  v_total_approved_count INTEGER;
BEGIN
  SELECT id, email, username, display_name, evidence_fingerprint_sha256,
         terms_version, privacy_version, status, sensitive_action_request_id,
         adult_verified_at
    INTO v_application
  FROM testagram_identity.identity_applications
  WHERE id = NEW.application_id
  FOR UPDATE;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'identity approval token requires an existing application';
  END IF;
  IF v_application.sensitive_action_request_id IS NULL THEN
    RAISE EXCEPTION 'identity approval token requires a linked approval request';
  END IF;
  IF v_application.adult_verified_at IS NULL THEN
    RAISE EXCEPTION 'verified adult evidence is required before identity approval token issuance';
  END IF;
  IF v_application.status <> 'approved' THEN
    RAISE EXCEPTION 'identity application must be approved before token issuance';
  END IF;

  v_request_id := v_application.sensitive_action_request_id;
  SELECT action_type, status, required_approvals, requested_by_appwrite_user_id, payload
    INTO v_action_type, v_request_status, v_required_approvals, v_requester, v_payload
  FROM testagram_auth.sensitive_action_requests
  WHERE id = v_request_id
  FOR UPDATE;

  IF NOT FOUND
     OR v_action_type <> 'identity.approve'
     OR v_request_status <> 'approved'
     OR v_required_approvals <> 2 THEN
    RAISE EXCEPTION 'linked sensitive-action request is not a completed two-person identity approval';
  END IF;

  IF v_requester = NEW.first_approver_appwrite_user_id
     OR v_requester = NEW.second_approver_appwrite_user_id THEN
    RAISE EXCEPTION 'approval requester cannot be an identity approver';
  END IF;

  IF v_payload->>'application_id' IS DISTINCT FROM NEW.application_id::text
     OR v_payload->>'email' IS DISTINCT FROM v_application.email
     OR v_payload->>'username' IS DISTINCT FROM v_application.username
     OR v_payload->>'display_name' IS DISTINCT FROM v_application.display_name
     OR v_payload->>'evidence_fingerprint_sha256' IS DISTINCT FROM encode(v_application.evidence_fingerprint_sha256, 'hex')
     OR v_payload->>'terms_version' IS DISTINCT FROM v_application.terms_version
     OR v_payload->>'privacy_version' IS DISTINCT FROM v_application.privacy_version THEN
    RAISE EXCEPTION 'approval payload does not match the current identity application';
  END IF;

  SELECT count(DISTINCT approver_appwrite_user_id)
    INTO v_total_approved_count
  FROM testagram_auth.sensitive_action_approvals
  WHERE request_id = v_request_id AND decision = 'approved';

  SELECT count(DISTINCT approver_appwrite_user_id)
    INTO v_approved_count
  FROM testagram_auth.sensitive_action_approvals
  WHERE request_id = v_request_id
    AND decision = 'approved'
    AND approver_appwrite_user_id IN (
      NEW.first_approver_appwrite_user_id,
      NEW.second_approver_appwrite_user_id
    );

  IF v_approved_count <> 2 OR v_total_approved_count <> 2 THEN
    RAISE EXCEPTION 'two distinct recorded approvals are required before token issuance';
  END IF;

  RETURN NEW;
END;
$function$;

DROP TRIGGER IF EXISTS identity_approval_token_requires_two_person_approval
  ON testagram_identity.identity_approval_tokens;

CREATE TRIGGER identity_approval_token_requires_two_person_approval
BEFORE INSERT ON testagram_identity.identity_approval_tokens
FOR EACH ROW EXECUTE FUNCTION testagram_identity.assert_two_person_identity_approval();

-- Durable worker leases and reconciliation metadata. External Appwrite calls
-- cannot be part of a PostgreSQL transaction, so ambiguous outcomes must be
-- recoverable and observable rather than treated as exactly-once operations.
ALTER TABLE testagram_identity.registration_provisioning
  ADD COLUMN IF NOT EXISTS lease_owner TEXT,
  ADD COLUMN IF NOT EXISTS lease_expires_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  ADD COLUMN IF NOT EXISTS last_attempt_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS completed_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS reconciliation_required BOOLEAN NOT NULL DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS last_error_class TEXT NOT NULL DEFAULT ''
    CHECK (length(last_error_class) <= 64);

DO $lease_check$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname = 'registration_provisioning_lease_pair_check'
      AND conrelid = 'testagram_identity.registration_provisioning'::regclass
  ) THEN
    ALTER TABLE testagram_identity.registration_provisioning
      ADD CONSTRAINT registration_provisioning_lease_pair_check
      CHECK ((lease_owner IS NULL) = (lease_expires_at IS NULL)) NOT VALID;
  END IF;
END;
$lease_check$;

CREATE INDEX IF NOT EXISTS registration_provisioning_worker_idx
  ON testagram_identity.registration_provisioning(next_attempt_at, created_at)
  WHERE status IN ('pending','appwrite_created','linked','verification_sent')
    AND reconciliation_required = FALSE;

CREATE INDEX IF NOT EXISTS registration_provisioning_reconciliation_idx
  ON testagram_identity.registration_provisioning(updated_at)
  WHERE reconciliation_required = TRUE OR status = 'compensation_required';

-- Consent evidence is append-only. Retention/deletion, if legally required,
-- must use a separate controlled process and must not rewrite acceptance data.
CREATE OR REPLACE FUNCTION testagram_identity.reject_consent_audit_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $function$
BEGIN
  RAISE EXCEPTION 'legal consent audit records are append-only';
END;
$function$;

DROP TRIGGER IF EXISTS legal_consent_audit_append_only
  ON testagram_identity.legal_consent_audit;

CREATE TRIGGER legal_consent_audit_append_only
BEFORE UPDATE OR DELETE ON testagram_identity.legal_consent_audit
FOR EACH ROW EXECUTE FUNCTION testagram_identity.reject_consent_audit_mutation();

REVOKE UPDATE, DELETE, TRUNCATE
  ON testagram_identity.legal_consent_audit
  FROM PUBLIC;

COMMENT ON COLUMN testagram_identity.identity_applications.submission_key_sha256 IS
  'SHA-256 of the client idempotency key; raw key is never persisted.';
COMMENT ON COLUMN testagram_identity.identity_applications.sensitive_action_request_id IS
  'References the canonical two-person sensitive-action approval request for this application.';
COMMENT ON COLUMN testagram_identity.registration_provisioning.reconciliation_required IS
  'True when an external Appwrite side effect has an ambiguous or incomplete outcome requiring operator/worker reconciliation.';

COMMIT;
