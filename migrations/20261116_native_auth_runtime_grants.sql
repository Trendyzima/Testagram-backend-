-- Runtime grants for Testagram native authentication.
-- Keep audit append-only for the application role; staff authorization tables
-- remain inaccessible until the dedicated native staff workflow is reviewed.
BEGIN;

DO $grants$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    GRANT USAGE ON SCHEMA testagram_auth TO testagram_app;
    GRANT SELECT,INSERT,UPDATE ON
      testagram_auth.identities,
      testagram_auth.sessions,
      testagram_auth.password_reset_tokens,
      testagram_auth.rate_limits
    TO testagram_app;
    GRANT SELECT,INSERT ON testagram_auth.security_audit TO testagram_app;
    GRANT USAGE,SELECT ON SEQUENCE testagram_auth.security_audit_event_id_seq TO testagram_app;
  END IF;

  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
    GRANT USAGE ON SCHEMA testagram_auth TO service_role;
    GRANT SELECT,INSERT,UPDATE ON
      testagram_auth.identities,
      testagram_auth.sessions,
      testagram_auth.password_reset_tokens,
      testagram_auth.rate_limits
    TO service_role;
    GRANT SELECT,INSERT ON testagram_auth.security_audit TO service_role;
    GRANT USAGE,SELECT ON SEQUENCE testagram_auth.security_audit_event_id_seq TO service_role;
  END IF;
END;
$grants$;

COMMIT;
