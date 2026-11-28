-- Revalidate current enrollment-approval permission at ceremony time.
-- Approval-time trigger checks are not sufficient: permissions can be revoked
-- after approval but before the target consumes the enrollment authorization.
BEGIN;

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

      AND (
        EXISTS (
          SELECT 1 FROM testagram_auth.staff_role_grants owner_role
          WHERE owner_role.user_id=first_approval.approver_user_id
            AND owner_role.role='owner' AND owner_role.revoked_at IS NULL
        )
        OR EXISTS (
          SELECT 1 FROM testagram_auth.native_staff_permission_overrides override
          WHERE override.user_id=first_approval.approver_user_id
            AND override.permission_key='staff.webauthn.enroll.approve'
            AND override.granted=TRUE
        )
        OR (
          NOT EXISTS (
            SELECT 1 FROM testagram_auth.native_staff_permission_overrides override
            WHERE override.user_id=first_approval.approver_user_id
              AND override.permission_key='staff.webauthn.enroll.approve'
          )
          AND EXISTS (
            SELECT 1
            FROM testagram_auth.staff_role_grants approver_role
            JOIN testagram_auth.staff_role_permissions role_permission
              ON role_permission.role=approver_role.role
            WHERE approver_role.user_id=first_approval.approver_user_id
              AND approver_role.revoked_at IS NULL
              AND role_permission.permission_key='staff.webauthn.enroll.approve'
          )
        )
      )
      AND (
        EXISTS (
          SELECT 1 FROM testagram_auth.staff_role_grants owner_role
          WHERE owner_role.user_id=second_approval.approver_user_id
            AND owner_role.role='owner' AND owner_role.revoked_at IS NULL
        )
        OR EXISTS (
          SELECT 1 FROM testagram_auth.native_staff_permission_overrides override
          WHERE override.user_id=second_approval.approver_user_id
            AND override.permission_key='staff.webauthn.enroll.approve'
            AND override.granted=TRUE
        )
        OR (
          NOT EXISTS (
            SELECT 1 FROM testagram_auth.native_staff_permission_overrides override
            WHERE override.user_id=second_approval.approver_user_id
              AND override.permission_key='staff.webauthn.enroll.approve'
          )
          AND EXISTS (
            SELECT 1
            FROM testagram_auth.staff_role_grants approver_role
            JOIN testagram_auth.staff_role_permissions role_permission
              ON role_permission.role=approver_role.role
            WHERE approver_role.user_id=second_approval.approver_user_id
              AND approver_role.revoked_at IS NULL
              AND role_permission.permission_key='staff.webauthn.enroll.approve'
          )
        )
      )
      AND NOT EXISTS (
        SELECT 1 FROM testagram_auth.staff_webauthn_credentials target_credential
        WHERE target_credential.user_id=target_staff.user_id
          AND target_credential.revoked_at IS NULL
      )
  )
$function$;

COMMIT;
