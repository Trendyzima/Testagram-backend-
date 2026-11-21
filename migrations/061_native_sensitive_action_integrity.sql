-- Harden the native sensitive-action ledger at the database boundary.
-- Request identity, target and payload are immutable after creation; every
-- approval must come from a currently enabled, MFA-enrolled native staff user.
BEGIN;

CREATE OR REPLACE FUNCTION testagram_auth.guard_native_sensitive_action_request_immutable()
RETURNS trigger
LANGUAGE plpgsql
AS $function$
DECLARE
  v_approved_count INTEGER;
  v_eligible_approved_count INTEGER;
  v_rejected_count INTEGER;
BEGIN
  IF NEW.action_type IS DISTINCT FROM OLD.action_type
     OR NEW.requested_by_user_id IS DISTINCT FROM OLD.requested_by_user_id
     OR NEW.requested_by_service IS DISTINCT FROM OLD.requested_by_service
     OR NEW.target_reference IS DISTINCT FROM OLD.target_reference
     OR NEW.reason IS DISTINCT FROM OLD.reason
     OR NEW.payload IS DISTINCT FROM OLD.payload
     OR NEW.payload_sha256 IS DISTINCT FROM OLD.payload_sha256
     OR NEW.required_approvals IS DISTINCT FROM OLD.required_approvals
     OR NEW.expires_at IS DISTINCT FROM OLD.expires_at
     OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'native sensitive-action request identity and payload are immutable';
  END IF;

  IF NEW.status IS DISTINCT FROM OLD.status THEN
    IF OLD.status<>'pending'
       OR NEW.status NOT IN ('approved','rejected','expired') THEN
      RAISE EXCEPTION 'native sensitive-action request status transition is invalid or terminal';
    END IF;
    IF NEW.status IN ('approved','rejected') AND NEW.decided_at IS NULL THEN
      RAISE EXCEPTION 'completed approval decisions require a decision timestamp';
    END IF;
    IF NEW.status='expired' AND OLD.expires_at>now() THEN
      RAISE EXCEPTION 'an approval request cannot be expired before its deadline';
    END IF;
  ELSIF NEW.decided_at IS DISTINCT FROM OLD.decided_at THEN
    RAISE EXCEPTION 'approval decision timestamp cannot change without a terminal status transition';
  END IF;

  -- Enforce the quorum at the database state transition itself. A caller
  -- cannot mark a request approved after only one signer, after expiry, or
  -- after a signer's staff identity/credential has been disabled or revoked.
  IF NEW.status='approved' THEN
    IF OLD.expires_at<=now() THEN
      RAISE EXCEPTION 'expired sensitive-action request cannot be approved';
    END IF;

    SELECT
      count(DISTINCT a.approver_user_id) FILTER (WHERE a.decision='approved'),
      count(DISTINCT a.approver_user_id) FILTER (
        WHERE a.decision='approved' AND EXISTS (
          SELECT 1
          FROM testagram_auth.staff_principals sp
          JOIN testagram_auth.identities i ON i.user_id=sp.user_id
          JOIN testagram_auth.staff_role_grants rg
            ON rg.user_id=sp.user_id AND rg.revoked_at IS NULL
          JOIN testagram_auth.staff_webauthn_credentials wc
            ON wc.user_id=sp.user_id
           AND wc.revoked_at IS NULL
           AND wc.user_verified=TRUE
           AND wc.clone_warning=FALSE
          WHERE sp.user_id=a.approver_user_id
            AND sp.enabled=TRUE
            AND sp.mfa_enrolled_at IS NOT NULL
            AND i.disabled_at IS NULL
            AND i.email_verified_at IS NOT NULL
        )
      ),
      count(*) FILTER (WHERE a.decision='rejected')
    INTO v_approved_count,v_eligible_approved_count,v_rejected_count
    FROM testagram_auth.native_sensitive_action_approvals a
    WHERE a.request_id=OLD.id;

    IF v_approved_count<>OLD.required_approvals
       OR v_eligible_approved_count<>OLD.required_approvals
       OR v_rejected_count<>0 THEN
      RAISE EXCEPTION 'native sensitive-action approval quorum is incomplete';
    END IF;
  END IF;

  RETURN NEW;
END;
$function$;

DROP TRIGGER IF EXISTS native_sensitive_action_request_immutable
  ON testagram_auth.native_sensitive_action_requests;
CREATE TRIGGER native_sensitive_action_request_immutable
  BEFORE UPDATE ON testagram_auth.native_sensitive_action_requests
  FOR EACH ROW
  EXECUTE FUNCTION testagram_auth.guard_native_sensitive_action_request_immutable();

CREATE OR REPLACE FUNCTION testagram_auth.assert_native_sensitive_action_approver()
RETURNS trigger
LANGUAGE plpgsql
AS $function$
DECLARE
  v_requester UUID;
  v_status TEXT;
  v_expires_at TIMESTAMPTZ;
  v_staff_eligible BOOLEAN;
BEGIN
  SELECT requested_by_user_id,status,expires_at
    INTO v_requester,v_status,v_expires_at
  FROM testagram_auth.native_sensitive_action_requests
  WHERE id=NEW.request_id
  FOR UPDATE;

  IF NOT FOUND OR v_status<>'pending' OR v_expires_at<=now() THEN
    RAISE EXCEPTION 'approval request is missing, expired or no longer pending';
  END IF;

  IF v_requester IS NOT NULL AND v_requester=NEW.approver_user_id THEN
    RAISE EXCEPTION 'requester cannot approve their own sensitive action';
  END IF;

  SELECT EXISTS (
    SELECT 1
    FROM testagram_auth.staff_principals sp
    JOIN testagram_auth.identities i ON i.user_id=sp.user_id
    JOIN testagram_auth.staff_role_grants rg
      ON rg.user_id=sp.user_id AND rg.revoked_at IS NULL
    JOIN testagram_auth.staff_webauthn_credentials wc
      ON wc.user_id=sp.user_id
     AND wc.revoked_at IS NULL
     AND wc.user_verified=TRUE
     AND wc.clone_warning=FALSE
    WHERE sp.user_id=NEW.approver_user_id
      AND sp.enabled=TRUE
      AND sp.mfa_enrolled_at IS NOT NULL
      AND i.disabled_at IS NULL
      AND i.email_verified_at IS NOT NULL
  ) INTO v_staff_eligible;

  IF NOT v_staff_eligible THEN
    RAISE EXCEPTION 'approver is not an enabled, verified and MFA-enrolled native staff principal';
  END IF;

  RETURN NEW;
END;
$function$;

DROP TRIGGER IF EXISTS native_sensitive_action_approver_guard
  ON testagram_auth.native_sensitive_action_approvals;
CREATE TRIGGER native_sensitive_action_approver_guard
  BEFORE INSERT ON testagram_auth.native_sensitive_action_approvals
  FOR EACH ROW
  EXECUTE FUNCTION testagram_auth.assert_native_sensitive_action_approver();

CREATE OR REPLACE FUNCTION testagram_auth.reject_native_sensitive_action_approval_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $function$
BEGIN
  RAISE EXCEPTION 'native sensitive-action approval decisions are append-only';
END;
$function$;

DROP TRIGGER IF EXISTS native_sensitive_action_approval_append_only
  ON testagram_auth.native_sensitive_action_approvals;
CREATE TRIGGER native_sensitive_action_approval_append_only
  BEFORE UPDATE OR DELETE ON testagram_auth.native_sensitive_action_approvals
  FOR EACH ROW
  EXECUTE FUNCTION testagram_auth.reject_native_sensitive_action_approval_mutation();

COMMENT ON FUNCTION testagram_auth.guard_native_sensitive_action_request_immutable() IS
  'Prevents changing a native approval request target, requester, action, payload digest or quorum after creation.';
COMMENT ON FUNCTION testagram_auth.assert_native_sensitive_action_approver() IS
  'Requires a pending unexpired request and an enabled, verified, MFA-enrolled native staff approver for every approval insert.';
COMMENT ON FUNCTION testagram_auth.reject_native_sensitive_action_approval_mutation() IS
  'Native sensitive-action decisions are immutable evidence and cannot be updated or deleted.';

COMMIT;
