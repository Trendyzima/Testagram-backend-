-- Retire Appwrite identity records from all application/service runtime roles.
-- Historical rows and already-applied migration files are preserved for audit;
-- the live application roles lose all access and no Appwrite client remains.
BEGIN;

DO $retire$
DECLARE
  role_name text;
BEGIN
  IF to_regnamespace('testagram_identity') IS NULL THEN
    RETURN;
  END IF;

  EXECUTE 'REVOKE ALL ON SCHEMA testagram_identity FROM PUBLIC';
  EXECUTE 'REVOKE ALL ON ALL TABLES IN SCHEMA testagram_identity FROM PUBLIC';
  EXECUTE 'REVOKE ALL ON ALL SEQUENCES IN SCHEMA testagram_identity FROM PUBLIC';

  FOREACH role_name IN ARRAY ARRAY['testagram_app','service_role'] LOOP
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname=role_name) THEN
      EXECUTE format('REVOKE ALL ON SCHEMA testagram_identity FROM %I',role_name);
      EXECUTE format('REVOKE ALL ON ALL TABLES IN SCHEMA testagram_identity FROM %I',role_name);
      EXECUTE format('REVOKE ALL ON ALL SEQUENCES IN SCHEMA testagram_identity FROM %I',role_name);
      EXECUTE format('ALTER DEFAULT PRIVILEGES IN SCHEMA testagram_identity REVOKE ALL ON TABLES FROM %I',role_name);
      EXECUTE format('ALTER DEFAULT PRIVILEGES IN SCHEMA testagram_identity REVOKE ALL ON SEQUENCES FROM %I',role_name);
    END IF;
  END LOOP;

  EXECUTE $comment$COMMENT ON SCHEMA testagram_identity IS
    'Retired legacy identity evidence only. Appwrite is not a supported runtime and application/service roles have no access.'$comment$;
END;
$retire$;

COMMIT;
