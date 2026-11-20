-- Unify identity evidence review, two-person account approval and provisioning
-- authorization on the native Testagram staff ledger.
BEGIN;

ALTER TABLE testagram_auth.native_sensitive_action_requests
  ADD COLUMN IF NOT EXISTS requested_by_service TEXT NOT NULL DEFAULT 'staff';
ALTER TABLE testagram_auth.native_sensitive_action_requests
  ALTER COLUMN requested_by_user_id DROP NOT NULL;
ALTER TABLE testagram_auth.native_sensitive_action_requests
  DROP CONSTRAINT IF EXISTS native_sensitive_action_requests_requester_source_check;
ALTER TABLE testagram_auth.native_sensitive_action_requests
  ADD CONSTRAINT native_sensitive_action_requests_requester_source_check
  CHECK (
    (requested_by_service = 'staff' AND requested_by_user_id IS NOT NULL)
    OR
    (requested_by_service = 'identity_pipeline' AND requested_by_user_id IS NULL)
  );

-- Existing reviewer rows are evidence-review history by default. The native
-- approval console marks its two independent account approvers explicitly.
ALTER TABLE testagram_identity.identity_application_reviews
  ADD COLUMN IF NOT EXISTS review_stage TEXT NOT NULL DEFAULT 'evidence_review';
ALTER TABLE testagram_identity.identity_application_reviews
  DROP CONSTRAINT IF EXISTS identity_application_reviews_review_stage_check;
ALTER TABLE testagram_identity.identity_application_reviews
  ADD CONSTRAINT identity_application_reviews_review_stage_check
  CHECK (review_stage IN ('evidence_review','account_approval'));
CREATE INDEX IF NOT EXISTS identity_application_reviews_stage_idx
  ON testagram_identity.identity_application_reviews
    (application_id,review_stage,decision,reviewer_user_id)
  WHERE reviewer_user_id IS NOT NULL;

-- A provisioning capability may only be initialized from the same native
-- approval request that the Admin Console has completed, with two distinct
-- account approvers who did not perform the evidence review.
CREATE OR REPLACE FUNCTION testagram_identity.assert_two_person_identity_approval()
RETURNS trigger
LANGUAGE plpgsql
AS $function$
DECLARE
  v_application RECORD;
  v_action_type TEXT;
  v_request_status TEXT;
  v_required_approvals SMALLINT;
  v_requester UUID;
  v_requester_service TEXT;
  v_target_reference TEXT;
  v_request_expires_at TIMESTAMPTZ;
  v_payload JSONB;
  v_approved_count INTEGER;
  v_matching_approved_count INTEGER;
  v_evidence_review_count INTEGER;
  v_evidence_reference_present BOOLEAN;
  v_account_review_count INTEGER;
  v_evidence_overlap_count INTEGER;
BEGIN
  IF NEW.first_approver_user_id IS NULL
     OR NEW.second_approver_user_id IS NULL
     OR NEW.first_approver_user_id = NEW.second_approver_user_id THEN
    RAISE EXCEPTION 'two distinct native staff approvers are required';
  END IF;

  SELECT id,email,username,display_name,evidence_fingerprint_sha256,
         terms_version,privacy_version,status,native_sensitive_action_request_id,
         adult_verified_at,terms_accepted_at,privacy_accepted_at
    INTO v_application
  FROM testagram_identity.identity_applications
  WHERE id=NEW.application_id
  FOR UPDATE;
  IF NOT FOUND OR v_application.native_sensitive_action_request_id IS NULL
     OR v_application.adult_verified_at IS NULL OR v_application.terms_accepted_at IS NULL
     OR v_application.privacy_accepted_at IS NULL OR v_application.status<>'approved' THEN
    RAISE EXCEPTION 'verified identity application is required before approval token issuance';
  END IF;

  SELECT action_type,status,required_approvals,requested_by_user_id,
         requested_by_service,target_reference,expires_at,payload
    INTO v_action_type,v_request_status,v_required_approvals,v_requester,
         v_requester_service,v_target_reference,v_request_expires_at,v_payload
  FROM testagram_auth.native_sensitive_action_requests
  WHERE id=v_application.native_sensitive_action_request_id
  FOR UPDATE;
  IF NOT FOUND OR v_action_type<>'identity.approve' OR v_request_status<>'approved'
     OR v_required_approvals<>2 OR v_requester IS NOT NULL
     OR v_requester_service<>'identity_pipeline'
     OR v_target_reference<>NEW.application_id::text OR v_request_expires_at<=now() THEN
    RAISE EXCEPTION 'linked request is not a completed native identity-pipeline approval';
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

  SELECT count(DISTINCT approver_user_id),
         count(DISTINCT approver_user_id)
           FILTER (WHERE approver_user_id IN (NEW.first_approver_user_id,NEW.second_approver_user_id))
    INTO v_approved_count,v_matching_approved_count
  FROM testagram_auth.native_sensitive_action_approvals
  WHERE request_id=v_application.native_sensitive_action_request_id
    AND decision='approved' AND approver_user_id IS NOT NULL;
  IF v_approved_count<>2 OR v_matching_approved_count<>2 THEN
    RAISE EXCEPTION 'exactly two distinct native approval decisions are required';
  END IF;

  SELECT count(DISTINCT reviewer_user_id),
         COALESCE(bool_or(evidence_audit_reference=v_payload->>'evidence_audit_reference'),FALSE)
    INTO v_evidence_review_count,v_evidence_reference_present
  FROM testagram_identity.identity_application_reviews
  WHERE application_id=NEW.application_id
    AND decision='approved' AND review_stage='evidence_review';
  IF v_evidence_review_count<>2 OR NOT v_evidence_reference_present THEN
    RAISE EXCEPTION 'two matching evidence reviews are required before account approval';
  END IF;

  SELECT count(DISTINCT reviewer_user_id)
    INTO v_account_review_count
  FROM testagram_identity.identity_application_reviews
  WHERE application_id=NEW.application_id
    AND decision='approved' AND review_stage='account_approval'
    AND reviewer_user_id IN (NEW.first_approver_user_id,NEW.second_approver_user_id);
  IF v_account_review_count<>2 THEN
    RAISE EXCEPTION 'native account-approval review records must match both approvers';
  END IF;

  SELECT count(DISTINCT reviewer_user_id)
    INTO v_evidence_overlap_count
  FROM testagram_identity.identity_application_reviews
  WHERE application_id=NEW.application_id
    AND decision='approved' AND review_stage='evidence_review'
    AND reviewer_user_id IN (NEW.first_approver_user_id,NEW.second_approver_user_id);
  IF v_evidence_overlap_count<>0 THEN
    RAISE EXCEPTION 'evidence reviewers cannot approve account activation';
  END IF;

  RETURN NEW;
END;
$function$;

COMMIT;
