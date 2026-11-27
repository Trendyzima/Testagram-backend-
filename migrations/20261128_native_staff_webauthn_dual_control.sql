-- Bind staff WebAuthn enrollment authorizations to the immutable two-person approval ledger.
-- Existing operator-bootstrap and historical rows remain readable; new dual-control
-- authorizations must reference an approved staff.webauthn.enroll request and both signers.
BEGIN;

ALTER TABLE testagram_auth.native_staff_lifecycle_events
  DROP CONSTRAINT IF EXISTS native_staff_lifecycle_events_action_check;
ALTER TABLE testagram_auth.native_staff_lifecycle_events
  ADD CONSTRAINT native_staff_lifecycle_events_action_check
  CHECK (action IN (
    'invitation_created',
    'role_granted',
    'role_revoked',
    'staff_disabled',
    'permission_changed',
    'webauthn_enrollment_authorized',
    'webauthn_enrollment_revoked',
    'invitation_accepted',
    'invitation_revoked'
  ));

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

CREATE OR REPLACE FUNCTION testagram_auth.staff_webauthn_enrollment_authorization_is_valid(
  p_authorization_id BIGINT,
  p_user_id UUID,
  p_rpid TEXT
)
RETURNS BOOLEAN
LANGUAGE SQL
STABLE
AS $function$
  SELECT EXISTS (
    SELECT 1
    FROM testagram_auth.staff_webauthn_enrollment_authorizations ea
    JOIN testagram_auth.native_sensitive_action_requests req
      ON req.id=ea.request_id
     AND req.action_type='staff.webauthn.enroll'
     AND req.status='approved'
     AND req.required_approvals=2
     AND req.target_reference=p_user_id::text
    JOIN testagram_auth.native_sensitive_action_approvals first_approval
      ON first_approval.request_id=req.id
     AND first_approval.approver_user_id=ea.first_approver_user_id
     AND first_approval.decision='approved'
     AND first_approval.webauthn_rpid=p_rpid
    JOIN testagram_auth.staff_principals first_staff
      ON first_staff.user_id=first_approval.approver_user_id
     AND first_staff.enabled=TRUE
     AND first_staff.mfa_enrolled_at IS NOT NULL
    JOIN testagram_auth.identities first_identity
      ON first_identity.user_id=first_staff.user_id
     AND first_identity.disabled_at IS NULL
     AND first_identity.email_verified_at IS NOT NULL
    JOIN testagram_auth.staff_role_grants first_role
      ON first_role.user_id=first_staff.user_id
     AND first_role.revoked_at IS NULL
    JOIN testagram_auth.staff_webauthn_credentials first_credential
      ON first_credential.user_id=first_staff.user_id
     AND first_credential.rpid=first_approval.webauthn_rpid
     AND first_credential.credential_id=first_approval.webauthn_credential_id
     AND first_credential.revoked_at IS NULL
     AND first_credential.user_verified=TRUE
     AND first_credential.clone_warning=FALSE
    JOIN testagram_auth.native_sensitive_action_approvals second_approval
      ON second_approval.request_id=req.id
     AND second_approval.approver_user_id=ea.second_approver_user_id
     AND second_approval.decision='approved'
     AND second_approval.approver_user_id<>first_approval.approver_user_id
     AND second_approval.webauthn_rpid=first_approval.webauthn_rpid
    JOIN testagram_auth.staff_principals second_staff
      ON second_staff.user_id=second_approval.approver_user_id
     AND second_staff.enabled=TRUE
     AND second_staff.mfa_enrolled_at IS NOT NULL
    JOIN testagram_auth.identities second_identity
      ON second_identity.user_id=second_staff.user_id
     AND second_identity.disabled_at IS NULL
     AND second_identity.email_verified_at IS NOT NULL
    JOIN testagram_auth.staff_role_grants second_role
      ON second_role.user_id=second_staff.user_id
     AND second_role.revoked_at IS NULL
    JOIN testagram_auth.staff_webauthn_credentials second_credential
      ON second_credential.user_id=second_staff.user_id
     AND second_credential.rpid=second_approval.webauthn_rpid
     AND second_credential.credential_id=second_approval.webauthn_credential_id
     AND second_credential.revoked_at IS NULL
     AND second_credential.user_verified=TRUE
     AND second_credential.clone_warning=FALSE
    JOIN testagram_auth.staff_principals target_staff
      ON target_staff.user_id=ea.user_id
     AND target_staff.enabled=TRUE
     AND target_staff.mfa_enrolled_at IS NULL
    JOIN testagram_auth.identities target_identity
      ON target_identity.user_id=target_staff.user_id
     AND target_identity.disabled_at IS NULL
     AND target_identity.email_verified_at IS NOT NULL
    JOIN testagram_auth.staff_role_grants target_role
      ON target_role.user_id=target_staff.user_id
     AND target_role.revoked_at IS NULL
    WHERE ea.id=p_authorization_id
      AND ea.user_id=p_user_id
      AND ea.authorization_method='dual_control'
      AND ea.request_id IS NOT NULL
      AND ea.first_approver_user_id<>ea.second_approver_user_id
      AND ea.consumed_at IS NULL
      AND ea.revoked_at IS NULL
      AND ea.expires_at>now()
      AND first_approval.approver_user_id<>target_staff.user_id
      AND second_approval.approver_user_id<>target_staff.user_id
      AND req.requested_by_user_id IS DISTINCT FROM first_approval.approver_user_id
      AND req.requested_by_user_id IS DISTINCT FROM second_approval.approver_user_id
      AND NOT EXISTS (
        SELECT 1 FROM testagram_auth.staff_webauthn_credentials target_credential
        WHERE target_credential.user_id=target_staff.user_id
          AND target_credential.revoked_at IS NULL
      )
  )
$function$;


-- The historical staff-role migration conditionally created this catalog only
-- while provider-keyed staff_roles still existed. That table is archived before
-- the native migration runner reaches 061, so create the native permission map
-- explicitly here rather than relying on the retired provider table.
INSERT INTO testagram_auth.admin_permissions(permission_key,description,sensitive)
VALUES ('staff.webauthn.enroll.approve',
        'Approve staff WebAuthn authenticator enrollment through independent dual control',TRUE)
ON CONFLICT (permission_key) DO UPDATE
SET description=EXCLUDED.description,sensitive=EXCLUDED.sensitive;

CREATE TABLE IF NOT EXISTS testagram_auth.staff_role_permissions (
  role TEXT NOT NULL CHECK (role IN ('owner','administrator','engineer','operations','support','moderator')),
  permission_key TEXT NOT NULL REFERENCES testagram_auth.admin_permissions(permission_key) ON DELETE CASCADE,
  PRIMARY KEY (role,permission_key)
);

INSERT INTO testagram_auth.staff_role_permissions(role,permission_key) VALUES
  ('administrator','identity.review'),
  ('administrator','moderation.manage'),
  ('administrator','advertising.manage'),
  ('administrator','marketplace.manage'),
  ('administrator','tv.manage'),
  ('administrator','engineering.read'),
  ('administrator','operations.manage'),
  ('administrator','audit.read'),
  ('administrator','staff.webauthn.enroll.approve'),
  ('engineer','engineering.read'),
  ('engineer','deployment.request'),
  ('operations','engineering.read'),
  ('operations','operations.manage'),
  ('operations','deployment.request'),
  ('support','moderation.manage'),
  ('moderator','moderation.manage')
ON CONFLICT DO NOTHING;

REVOKE ALL ON testagram_auth.staff_role_permissions FROM PUBLIC;
DO $permission_grants$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    GRANT SELECT ON testagram_auth.staff_role_permissions TO testagram_app;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
    GRANT SELECT ON testagram_auth.staff_role_permissions TO service_role;
  END IF;
END;
$permission_grants$;

CREATE OR REPLACE FUNCTION testagram_auth.assert_staff_webauthn_enrollment_approver_permission()
RETURNS trigger
LANGUAGE plpgsql
AS $function$
DECLARE
  v_action_type TEXT;
  v_override BOOLEAN;
  v_allowed BOOLEAN := FALSE;
BEGIN
  SELECT action_type INTO v_action_type
  FROM testagram_auth.native_sensitive_action_requests
  WHERE id=NEW.request_id;
  IF v_action_type IS DISTINCT FROM 'staff.webauthn.enroll' THEN
    RETURN NEW;
  END IF;

  IF EXISTS (
    SELECT 1 FROM testagram_auth.staff_role_grants
    WHERE user_id=NEW.approver_user_id
      AND role='owner' AND revoked_at IS NULL
  ) THEN
    RETURN NEW;
  END IF;

  SELECT granted INTO v_override
  FROM testagram_auth.native_staff_permission_overrides
  WHERE user_id=NEW.approver_user_id
    AND permission_key='staff.webauthn.enroll.approve';
  IF FOUND THEN
    v_allowed := v_override;
  ELSE
    SELECT EXISTS (
      SELECT 1
      FROM testagram_auth.staff_role_grants rg
      JOIN testagram_auth.staff_role_permissions rp ON rp.role=rg.role
      WHERE rg.user_id=NEW.approver_user_id
        AND rg.revoked_at IS NULL
        AND rp.permission_key='staff.webauthn.enroll.approve'
    ) INTO v_allowed;
  END IF;

  IF NOT v_allowed THEN
    RAISE EXCEPTION 'staff WebAuthn enrollment approver lacks staff.manage permission';
  END IF;
  RETURN NEW;
END;
$function$;

DROP TRIGGER IF EXISTS native_staff_webauthn_enrollment_approver_permission
  ON testagram_auth.native_sensitive_action_approvals;
CREATE TRIGGER native_staff_webauthn_enrollment_approver_permission
  BEFORE INSERT ON testagram_auth.native_sensitive_action_approvals
  FOR EACH ROW
  EXECUTE FUNCTION testagram_auth.assert_staff_webauthn_enrollment_approver_permission();

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
