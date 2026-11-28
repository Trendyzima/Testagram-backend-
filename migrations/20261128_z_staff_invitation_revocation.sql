-- Add the controlled two-person staff invitation revocation action.
-- The existing token digest and acceptance checks remain unchanged; revocation
-- commits atomically with the second independent WebAuthn approval.
BEGIN;
ALTER TABLE testagram_auth.native_sensitive_action_requests
  DROP CONSTRAINT IF EXISTS native_sensitive_action_requests_action_type_check;
ALTER TABLE testagram_auth.native_sensitive_action_requests
  ADD CONSTRAINT native_sensitive_action_requests_action_type_check
  CHECK (action_type IN (
    'identity.approve','finance.execute','security.change','deployment.production',
    'staff.invite','staff.invitation.revoke','staff.role_change','staff.revoke',
    'staff.permission_change','staff.webauthn.enroll'
  ));
COMMENT ON COLUMN testagram_auth.native_staff_invitations.revoked_at IS
  'Set only by the atomic two-person staff.invitation.revoke executor; revoked tokens can never be accepted.';
COMMIT;
