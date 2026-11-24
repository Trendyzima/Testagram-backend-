-- Additive bridge: keep the identity-approval request/audit history intact while
-- making every reviewer decision and approval-token signer a native Testagram UUID.
BEGIN;

-- The native admin-console approval workflow has its own UUID request ledger.
-- Keep the historical API approval link intact; do not overload its foreign key.
ALTER TABLE testagram_identity.identity_applications
  ADD COLUMN IF NOT EXISTS native_sensitive_action_request_id UUID
    REFERENCES testagram_auth.native_sensitive_action_requests(id) ON DELETE RESTRICT;
CREATE INDEX IF NOT EXISTS identity_applications_native_sensitive_request_idx
  ON testagram_identity.identity_applications(native_sensitive_action_request_id)
  WHERE native_sensitive_action_request_id IS NOT NULL;



-- Preserve native UUID attribution on historical provider-keyed ledgers when
-- they exist. Native-only installations have no legacy tables to alter.
DO $legacy_admin_ledger_bridge$
BEGIN
  IF to_regclass('testagram_auth.sensitive_action_approvals') IS NOT NULL THEN
    EXECUTE 'ALTER TABLE testagram_auth.sensitive_action_approvals
      ADD COLUMN IF NOT EXISTS approver_user_id UUID
      REFERENCES testagram_auth.staff_principals(user_id) ON DELETE RESTRICT';
    EXECUTE 'ALTER TABLE testagram_auth.sensitive_action_approvals
      ALTER COLUMN approver_appwrite_user_id DROP NOT NULL';
    EXECUTE 'CREATE UNIQUE INDEX IF NOT EXISTS sensitive_action_approvals_native_reviewer_uidx
      ON testagram_auth.sensitive_action_approvals(request_id,approver_user_id)
      WHERE approver_user_id IS NOT NULL';
  END IF;

  IF to_regclass('testagram_auth.admin_security_audit') IS NOT NULL THEN
    EXECUTE 'ALTER TABLE testagram_auth.admin_security_audit
      ADD COLUMN IF NOT EXISTS actor_user_id UUID
      REFERENCES testagram_auth.identities(user_id) ON DELETE RESTRICT';
    EXECUTE 'ALTER TABLE testagram_auth.admin_security_audit
      ALTER COLUMN actor_appwrite_user_id DROP NOT NULL';
  END IF;
END;
$legacy_admin_ledger_bridge$;

ALTER TABLE testagram_identity.identity_approval_tokens
  ADD COLUMN IF NOT EXISTS first_approver_user_id UUID
    REFERENCES testagram_auth.staff_principals(user_id) ON DELETE RESTRICT,
  ADD COLUMN IF NOT EXISTS second_approver_user_id UUID
    REFERENCES testagram_auth.staff_principals(user_id) ON DELETE RESTRICT;
ALTER TABLE testagram_identity.identity_approval_tokens
  ALTER COLUMN first_approver_appwrite_user_id DROP NOT NULL,
  ALTER COLUMN second_approver_appwrite_user_id DROP NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS identity_approval_tokens_native_signers_uidx
  ON testagram_identity.identity_approval_tokens(application_id,first_approver_user_id,second_approver_user_id)
  WHERE first_approver_user_id IS NOT NULL AND second_approver_user_id IS NOT NULL;

-- Replace the old provider-keyed trigger with the same strict two-person,
-- snapshot-bound invariant over native reviewer UUIDs.
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
  v_review_count INTEGER;
BEGIN
  IF NEW.first_approver_user_id IS NULL
     OR NEW.second_approver_user_id IS NULL
     OR NEW.first_approver_user_id = NEW.second_approver_user_id THEN
    RAISE EXCEPTION 'two distinct native staff approvers are required';
  END IF;

  SELECT id,email,username,display_name,evidence_fingerprint_sha256,
         terms_version,privacy_version,status,sensitive_action_request_id,adult_verified_at
    INTO v_application
  FROM testagram_identity.identity_applications
  WHERE id=NEW.application_id
  FOR UPDATE;
  IF NOT FOUND OR v_application.sensitive_action_request_id IS NULL
     OR v_application.adult_verified_at IS NULL OR v_application.status<>'approved' THEN
    RAISE EXCEPTION 'verified identity application is required before approval token issuance';
  END IF;

  v_request_id:=v_application.sensitive_action_request_id;
  SELECT action_type,status,required_approvals,requested_by_appwrite_user_id,payload
    INTO v_action_type,v_request_status,v_required_approvals,v_requester,v_payload
  FROM testagram_auth.sensitive_action_requests
  WHERE id=v_request_id FOR UPDATE;
  IF NOT FOUND OR v_action_type<>'identity.approve' OR v_request_status<>'approved'
     OR v_required_approvals<>2 THEN
    RAISE EXCEPTION 'linked request is not a completed two-person identity approval';
  END IF;
  IF v_requester=NEW.first_approver_user_id::text
     OR v_requester=NEW.second_approver_user_id::text THEN
    RAISE EXCEPTION 'approval requester cannot be an identity approver';
  END IF;
  IF v_payload->>'application_id' IS DISTINCT FROM NEW.application_id::text
     OR v_payload->>'email' IS DISTINCT FROM v_application.email
     OR v_payload->>'username' IS DISTINCT FROM v_application.username
     OR v_payload->>'display_name' IS DISTINCT FROM v_application.display_name
     OR v_payload->>'evidence_fingerprint_sha256' IS DISTINCT FROM encode(v_application.evidence_fingerprint_sha256,'hex')
     OR v_payload->>'terms_version' IS DISTINCT FROM v_application.terms_version
     OR v_payload->>'privacy_version' IS DISTINCT FROM v_application.privacy_version THEN
    RAISE EXCEPTION 'approval payload does not match the current identity application';
  END IF;

  SELECT count(DISTINCT approver_user_id),count(DISTINCT approver_user_id)
    FILTER (WHERE approver_user_id IN (NEW.first_approver_user_id,NEW.second_approver_user_id))
    INTO v_total_approved_count,v_approved_count
  FROM testagram_auth.sensitive_action_approvals
  WHERE request_id=v_request_id AND decision='approved' AND approver_user_id IS NOT NULL;
  IF v_approved_count<>2 OR v_total_approved_count<>2 THEN
    RAISE EXCEPTION 'exactly two distinct native approval decisions are required';
  END IF;

  SELECT count(DISTINCT reviewer_user_id) INTO v_review_count
  FROM testagram_identity.identity_application_reviews
  WHERE application_id=NEW.application_id AND decision='approved'
    AND reviewer_user_id IN (NEW.first_approver_user_id,NEW.second_approver_user_id);
  IF v_review_count<>2 THEN
    RAISE EXCEPTION 'native identity review records must match both approvers';
  END IF;
  RETURN NEW;
END;
$function$;

REVOKE ALL ON TABLE testagram_identity.identity_approval_tokens FROM PUBLIC;
DO $legacy_admin_ledger_revoke$
BEGIN
  IF to_regclass('testagram_auth.sensitive_action_approvals') IS NOT NULL THEN
    EXECUTE 'REVOKE ALL ON TABLE testagram_auth.sensitive_action_approvals FROM PUBLIC';
  END IF;
  IF to_regclass('testagram_auth.admin_security_audit') IS NOT NULL THEN
    EXECUTE 'REVOKE ALL ON TABLE testagram_auth.admin_security_audit FROM PUBLIC';
  END IF;
END;
$legacy_admin_ledger_revoke$;
DO $grants$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    IF to_regclass('testagram_auth.sensitive_action_approvals') IS NOT NULL THEN
      EXECUTE 'GRANT SELECT,INSERT ON testagram_auth.sensitive_action_approvals TO testagram_app';
      EXECUTE 'GRANT USAGE,SELECT ON SEQUENCE testagram_auth.sensitive_action_approvals_id_seq TO testagram_app';
    END IF;
    IF to_regclass('testagram_auth.admin_security_audit') IS NOT NULL THEN
      EXECUTE 'GRANT SELECT,INSERT ON testagram_auth.admin_security_audit TO testagram_app';
      EXECUTE 'GRANT USAGE,SELECT ON SEQUENCE testagram_auth.admin_security_audit_id_seq TO testagram_app';
    END IF;
    GRANT SELECT,INSERT,UPDATE ON testagram_identity.identity_approval_tokens TO testagram_app;
  END IF;
END;
$grants$;

COMMIT;
