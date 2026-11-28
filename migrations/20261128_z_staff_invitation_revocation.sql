-- Add the controlled two-person staff invitation revocation action.
-- Revocation is bound to the immutable approval request at both application and
-- database boundaries. Existing unlinked revoked rows require explicit audit reconciliation.
BEGIN;

ALTER TABLE testagram_auth.native_staff_invitations
  ADD COLUMN IF NOT EXISTS revoked_by_request_id UUID
    REFERENCES testagram_auth.native_sensitive_action_requests(id) ON DELETE RESTRICT;

DO $preflight$
BEGIN
  IF EXISTS (
    SELECT 1 FROM testagram_auth.native_staff_invitations
    WHERE revoked_at IS NOT NULL AND revoked_by_request_id IS NULL
  ) THEN
    RAISE EXCEPTION 'Existing revoked staff invitations require audit reconciliation before enabling guarded revocation';
  END IF;
END;
$preflight$;

ALTER TABLE testagram_auth.native_staff_invitations
  DROP CONSTRAINT IF EXISTS native_staff_invitation_revocation_request_pair_check;
ALTER TABLE testagram_auth.native_staff_invitations
  ADD CONSTRAINT native_staff_invitation_revocation_request_pair_check
  CHECK ((revoked_at IS NULL) = (revoked_by_request_id IS NULL));

ALTER TABLE testagram_auth.native_sensitive_action_requests
  DROP CONSTRAINT IF EXISTS native_sensitive_action_requests_action_type_check;
ALTER TABLE testagram_auth.native_sensitive_action_requests
  ADD CONSTRAINT native_sensitive_action_requests_action_type_check
  CHECK (action_type IN (
    'identity.approve','finance.execute','security.change','deployment.production',
    'staff.invite','staff.invitation.revoke','staff.role_change','staff.revoke',
    'staff.permission_change','staff.webauthn.enroll'
  ));

CREATE OR REPLACE FUNCTION testagram_auth.guard_native_staff_invitation_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $function$
DECLARE
  v_target_reference TEXT;
  v_original_target TEXT;
  v_original_requester UUID;
  v_requester UUID;
  v_target UUID;
  v_requester_eligible BOOLEAN;
  v_total_approvals INTEGER;
  v_eligible_approvals INTEGER;
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION 'native staff invitations are immutable lifecycle records';
  END IF;

  IF NEW.id IS DISTINCT FROM OLD.id
     OR NEW.request_id IS DISTINCT FROM OLD.request_id
     OR NEW.email_normalized IS DISTINCT FROM OLD.email_normalized
     OR NEW.role IS DISTINCT FROM OLD.role
     OR NEW.token_sha256 IS DISTINCT FROM OLD.token_sha256
     OR NEW.created_by IS DISTINCT FROM OLD.created_by
     OR NEW.created_at IS DISTINCT FROM OLD.created_at
     OR NEW.expires_at IS DISTINCT FROM OLD.expires_at THEN
    RAISE EXCEPTION 'native staff invitation identity and token fields are immutable';
  END IF;

  IF OLD.accepted_at IS NOT NULL
     AND (NEW.accepted_at IS DISTINCT FROM OLD.accepted_at
       OR NEW.accepted_by_user_id IS DISTINCT FROM OLD.accepted_by_user_id) THEN
    RAISE EXCEPTION 'accepted staff invitation state is terminal';
  END IF;

  IF OLD.revoked_at IS NOT NULL
     AND (NEW.revoked_at IS DISTINCT FROM OLD.revoked_at
       OR NEW.revoked_by_request_id IS DISTINCT FROM OLD.revoked_by_request_id) THEN
    RAISE EXCEPTION 'revoked staff invitation state is terminal';
  END IF;

  IF NEW.revoked_at IS NOT NULL AND OLD.revoked_at IS NULL THEN
    IF NEW.revoked_by_request_id IS NULL THEN
      RAISE EXCEPTION 'staff invitation revocation requires its two-person request reference';
    END IF;

    SELECT req.target_reference,req.requested_by_user_id
      INTO v_target_reference,v_requester
    FROM testagram_auth.native_sensitive_action_requests req
    WHERE req.id=NEW.revoked_by_request_id
      AND req.action_type='staff.invitation.revoke'
      AND req.status='approved'
      AND req.required_approvals=2
      AND req.payload->>'invitation_id'=OLD.id::text;
    IF NOT FOUND OR v_requester IS NULL THEN
      RAISE EXCEPTION 'staff invitation revocation is not bound to an approved two-person request';
    END IF;

    SELECT original.target_reference,original.requested_by_user_id
      INTO v_original_target,v_original_requester
    FROM testagram_auth.native_sensitive_action_requests original
    WHERE original.id=OLD.request_id
      AND original.action_type='staff.invite'
      AND original.status='approved';
    IF NOT FOUND
       OR v_original_target IS DISTINCT FROM v_target_reference
       OR v_original_requester IS DISTINCT FROM OLD.created_by THEN
      RAISE EXCEPTION 'revocation target does not match the approved original invitation';
    END IF;

    v_target := v_target_reference::uuid;
    IF v_requester=v_target THEN
      RAISE EXCEPTION 'staff invitation revocation requester cannot be its target';
    END IF;

    SELECT EXISTS (
      SELECT 1
      FROM testagram_auth.staff_principals sp
      JOIN testagram_auth.identities i ON i.user_id=sp.user_id
      JOIN testagram_auth.staff_role_grants rg ON rg.user_id=sp.user_id
      WHERE sp.user_id=v_requester AND sp.enabled=TRUE AND sp.mfa_enrolled_at IS NOT NULL
        AND i.disabled_at IS NULL AND i.email_verified_at IS NOT NULL
        AND rg.role='owner' AND rg.revoked_at IS NULL
        AND EXISTS (
          SELECT 1 FROM testagram_auth.staff_webauthn_credentials wc
          WHERE wc.user_id=sp.user_id AND wc.revoked_at IS NULL
            AND wc.user_verified=TRUE AND wc.clone_warning=FALSE
        )
    ) INTO v_requester_eligible;
    IF NOT v_requester_eligible THEN
      RAISE EXCEPTION 'staff invitation revocation requester is not a currently eligible owner';
    END IF;

    SELECT count(DISTINCT approver_user_id)
      INTO v_total_approvals
    FROM testagram_auth.native_sensitive_action_approvals
    WHERE request_id=NEW.revoked_by_request_id AND decision='approved';

    SELECT count(DISTINCT a.approver_user_id)
      INTO v_eligible_approvals
    FROM testagram_auth.native_sensitive_action_approvals a
    JOIN testagram_auth.staff_principals sp
      ON sp.user_id=a.approver_user_id AND sp.enabled=TRUE AND sp.mfa_enrolled_at IS NOT NULL
    JOIN testagram_auth.identities i
      ON i.user_id=sp.user_id AND i.disabled_at IS NULL AND i.email_verified_at IS NOT NULL
    JOIN testagram_auth.staff_role_grants rg
      ON rg.user_id=sp.user_id AND rg.role='owner' AND rg.revoked_at IS NULL
    JOIN testagram_auth.staff_webauthn_credentials wc
      ON wc.user_id=a.approver_user_id
     AND wc.rpid=a.webauthn_rpid
     AND wc.credential_id=a.webauthn_credential_id
     AND wc.revoked_at IS NULL AND wc.user_verified=TRUE AND wc.clone_warning=FALSE
    WHERE a.request_id=NEW.revoked_by_request_id
      AND a.decision='approved'
      AND a.approver_user_id<>v_requester
      AND a.approver_user_id<>v_target;

    IF v_total_approvals<>2 OR v_eligible_approvals<>2 THEN
      RAISE EXCEPTION 'staff invitation revocation requires exactly two independent eligible owner WebAuthn approvals';
    END IF;
  END IF;

  RETURN NEW;
END;
$function$;

DROP TRIGGER IF EXISTS native_staff_invitation_mutation_guard
  ON testagram_auth.native_staff_invitations;
CREATE TRIGGER native_staff_invitation_mutation_guard
  BEFORE UPDATE OR DELETE ON testagram_auth.native_staff_invitations
  FOR EACH ROW EXECUTE FUNCTION testagram_auth.guard_native_staff_invitation_mutation();

COMMENT ON COLUMN testagram_auth.native_staff_invitations.revoked_at IS
  'Set only by a database-guarded two-person staff.invitation.revoke request; revoked tokens can never be accepted.';
COMMENT ON COLUMN testagram_auth.native_staff_invitations.revoked_by_request_id IS
  'Immutable reference to the approved two-person request that revoked this invitation.';

COMMIT;
