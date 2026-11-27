-- Bind staff WebAuthn enrollment authorizations to the immutable two-person approval ledger.
-- Existing operator-bootstrap and historical rows remain readable; new dual-control
-- authorizations must reference an approved staff.webauthn.enroll request and both signers.
BEGIN;

ALTER TABLE testagram_auth.staff_webauthn_enrollment_authorizations
  ADD COLUMN IF NOT EXISTS request_id UUID
    REFERENCES testagram_auth.native_sensitive_action_requests(id) ON DELETE RESTRICT,
  ADD COLUMN IF NOT EXISTS first_approver_user_id UUID
    REFERENCES testagram_auth.staff_principals(user_id) ON DELETE RESTRICT,
  ADD COLUMN IF NOT EXISTS second_approver_user_id UUID
    REFERENCES testagram_auth.staff_principals(user_id) ON DELETE RESTRICT,
  ADD COLUMN IF NOT EXISTS revoked_at TIMESTAMPTZ;

ALTER TABLE testagram_auth.staff_webauthn_enrollment_authorizations
  DROP CONSTRAINT IF EXISTS staff_webauthn_enrollment_dual_control_ledger_check;
ALTER TABLE testagram_auth.staff_webauthn_enrollment_authorizations
  ADD CONSTRAINT staff_webauthn_enrollment_dual_control_ledger_check
  CHECK (
    request_id IS NULL OR (
      authorization_method='dual_control'
      AND first_approver_user_id IS NOT NULL
      AND second_approver_user_id IS NOT NULL
      AND first_approver_user_id<>second_approver_user_id
      AND authorized_by=first_approver_user_id
    )
  );

CREATE UNIQUE INDEX IF NOT EXISTS staff_webauthn_enrollment_request_uidx
  ON testagram_auth.staff_webauthn_enrollment_authorizations(request_id)
  WHERE request_id IS NOT NULL;

DROP INDEX IF EXISTS testagram_auth.staff_webauthn_enrollment_one_pending_idx;
CREATE UNIQUE INDEX staff_webauthn_enrollment_one_pending_idx
  ON testagram_auth.staff_webauthn_enrollment_authorizations(user_id)
  WHERE consumed_at IS NULL AND revoked_at IS NULL;

CREATE INDEX IF NOT EXISTS staff_webauthn_enrollment_request_target_idx
  ON testagram_auth.staff_webauthn_enrollment_authorizations(request_id,user_id)
  WHERE request_id IS NOT NULL;

CREATE OR REPLACE FUNCTION testagram_auth.guard_staff_webauthn_enrollment_authorization()
RETURNS trigger
LANGUAGE plpgsql
AS $function$
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION 'staff WebAuthn enrollment authorizations are immutable audit evidence';
  END IF;
  IF NEW.id IS DISTINCT FROM OLD.id
     OR NEW.user_id IS DISTINCT FROM OLD.user_id
     OR NEW.authorized_by IS DISTINCT FROM OLD.authorized_by
     OR NEW.authorization_method IS DISTINCT FROM OLD.authorization_method
     OR NEW.reason IS DISTINCT FROM OLD.reason
     OR NEW.created_at IS DISTINCT FROM OLD.created_at
     OR NEW.expires_at IS DISTINCT FROM OLD.expires_at
     OR NEW.request_id IS DISTINCT FROM OLD.request_id
     OR NEW.first_approver_user_id IS DISTINCT FROM OLD.first_approver_user_id
     OR NEW.second_approver_user_id IS DISTINCT FROM OLD.second_approver_user_id THEN
    RAISE EXCEPTION 'staff WebAuthn enrollment authorization identity is immutable';
  END IF;
  IF OLD.consumed_at IS NOT NULL AND NEW.consumed_at IS DISTINCT FROM OLD.consumed_at THEN
    RAISE EXCEPTION 'consumed staff WebAuthn enrollment authorization is terminal';
  END IF;
  IF OLD.revoked_at IS NOT NULL AND NEW.revoked_at IS DISTINCT FROM OLD.revoked_at THEN
    RAISE EXCEPTION 'revoked staff WebAuthn enrollment authorization is terminal';
  END IF;
  IF NEW.consumed_at IS NOT NULL AND OLD.consumed_at IS NULL
     AND (OLD.revoked_at IS NOT NULL OR OLD.expires_at<=now()) THEN
    RAISE EXCEPTION 'expired or revoked staff WebAuthn enrollment authorization cannot be consumed';
  END IF;
  IF NEW.revoked_at IS NOT NULL AND OLD.revoked_at IS NULL
     AND OLD.consumed_at IS NOT NULL THEN
    RAISE EXCEPTION 'consumed staff WebAuthn enrollment authorization cannot be revoked';
  END IF;
  RETURN NEW;
END;
$function$;

DROP TRIGGER IF EXISTS staff_webauthn_enrollment_authorization_guard
  ON testagram_auth.staff_webauthn_enrollment_authorizations;
CREATE TRIGGER staff_webauthn_enrollment_authorization_guard
  BEFORE UPDATE OR DELETE ON testagram_auth.staff_webauthn_enrollment_authorizations
  FOR EACH ROW
  EXECUTE FUNCTION testagram_auth.guard_staff_webauthn_enrollment_authorization();

REVOKE UPDATE ON testagram_auth.staff_webauthn_enrollment_authorizations FROM PUBLIC;
DO $grants$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    REVOKE UPDATE ON testagram_auth.staff_webauthn_enrollment_authorizations FROM testagram_app;
    GRANT UPDATE (consumed_at,revoked_at)
      ON testagram_auth.staff_webauthn_enrollment_authorizations TO testagram_app;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
    REVOKE UPDATE ON testagram_auth.staff_webauthn_enrollment_authorizations FROM service_role;
    GRANT UPDATE (consumed_at,revoked_at)
      ON testagram_auth.staff_webauthn_enrollment_authorizations TO service_role;
  END IF;
END;
$grants$;

COMMENT ON TABLE testagram_auth.staff_webauthn_enrollment_authorizations IS
  'Short-lived, single-use staff WebAuthn enrollment authorizations bound to an immutable two-person native approval request.';

COMMIT;
