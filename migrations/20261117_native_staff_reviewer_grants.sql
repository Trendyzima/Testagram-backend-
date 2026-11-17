-- Permit the Backend API to authorize native staff reviewers.
-- Staff assignments remain disabled by default and require explicit operator provisioning.
BEGIN;

REVOKE ALL ON TABLE
  testagram_auth.staff_principals,
  testagram_auth.staff_role_grants
FROM PUBLIC;

DO $grants$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    GRANT SELECT ON
      testagram_auth.staff_principals,
      testagram_auth.staff_role_grants
    TO testagram_app;
  END IF;

  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
    GRANT SELECT ON
      testagram_auth.staff_principals,
      testagram_auth.staff_role_grants
    TO service_role;
  END IF;
END;
$grants$;

COMMENT ON TABLE testagram_auth.staff_principals IS
  'Testagram-native staff identities. Reviewer access requires enabled=true and enrolled MFA.';
COMMENT ON TABLE testagram_auth.staff_role_grants IS
  'Testagram-native staff role grants; active grants have revoked_at IS NULL.';

COMMIT;
