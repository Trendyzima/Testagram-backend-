-- Move old provider-keyed staff assignments out of the live authorization namespace.
-- Foreign keys from historical review rows follow the table/column renames and
-- keep their provenance. Runtime authorization uses staff_principals/role_grants.
BEGIN;

DO $rename_tables$
BEGIN
  IF to_regclass('testagram_auth.staff_roles') IS NOT NULL
     AND to_regclass('testagram_auth.historical_external_staff_roles') IS NULL THEN
    ALTER TABLE testagram_auth.staff_roles RENAME TO historical_external_staff_roles;
  ELSIF to_regclass('testagram_auth.staff_roles') IS NOT NULL
     AND to_regclass('testagram_auth.historical_external_staff_roles') IS NOT NULL THEN
    RAISE EXCEPTION 'both historical and live legacy staff-role tables exist; reconcile explicitly';
  END IF;

  IF to_regclass('testagram_auth.staff_role_audit') IS NOT NULL
     AND to_regclass('testagram_auth.historical_external_staff_role_audit') IS NULL THEN
    ALTER TABLE testagram_auth.staff_role_audit RENAME TO historical_external_staff_role_audit;
  ELSIF to_regclass('testagram_auth.staff_role_audit') IS NOT NULL
     AND to_regclass('testagram_auth.historical_external_staff_role_audit') IS NOT NULL THEN
    RAISE EXCEPTION 'both historical and live legacy staff-role audit tables exist; reconcile explicitly';
  END IF;
END;
$rename_tables$;

DO $rename_columns$
BEGIN
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_auth' AND table_name='historical_external_staff_roles' AND column_name='appwrite_user_id') THEN
    ALTER TABLE testagram_auth.historical_external_staff_roles RENAME COLUMN appwrite_user_id TO source_staff_id;
  END IF;
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_auth' AND table_name='historical_external_staff_roles' AND column_name='created_by_appwrite_user_id') THEN
    ALTER TABLE testagram_auth.historical_external_staff_roles RENAME COLUMN created_by_appwrite_user_id TO source_created_by_staff_id;
  END IF;
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_auth' AND table_name='historical_external_staff_role_audit' AND column_name='actor_appwrite_user_id') THEN
    ALTER TABLE testagram_auth.historical_external_staff_role_audit RENAME COLUMN actor_appwrite_user_id TO source_actor_staff_id;
  END IF;
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_auth' AND table_name='historical_external_staff_role_audit' AND column_name='target_appwrite_user_id') THEN
    ALTER TABLE testagram_auth.historical_external_staff_role_audit RENAME COLUMN target_appwrite_user_id TO source_target_staff_id;
  END IF;
END;
$rename_columns$;

DO $indexes$
BEGIN
  IF to_regclass('testagram_auth.staff_roles_role_idx') IS NOT NULL THEN
    ALTER INDEX testagram_auth.staff_roles_role_idx RENAME TO historical_external_staff_roles_role_idx;
  END IF;
  IF to_regclass('testagram_auth.staff_roles_active_role_idx') IS NOT NULL THEN
    ALTER INDEX testagram_auth.staff_roles_active_role_idx RENAME TO historical_external_staff_roles_active_role_idx;
  END IF;
  IF to_regclass('testagram_auth.staff_role_audit_target_created_idx') IS NOT NULL THEN
    ALTER INDEX testagram_auth.staff_role_audit_target_created_idx RENAME TO historical_external_staff_role_audit_target_created_idx;
  END IF;
END;
$indexes$;

DO $rename_historical_identifiers$
DECLARE
  item RECORD;
  replacement_name TEXT;
BEGIN
  FOR item IN
    SELECT conrelid::regclass AS relation_name, conname
    FROM pg_constraint
    WHERE conrelid IN (
      'testagram_auth.historical_external_staff_roles'::regclass,
      'testagram_auth.historical_external_staff_role_audit'::regclass
    )
      AND conname ILIKE '%appwrite%'
  LOOP
    replacement_name := replace(item.conname,'appwrite','source');
    EXECUTE format('ALTER TABLE %s RENAME CONSTRAINT %I TO %I',
      item.relation_name,item.conname,replacement_name);
  END LOOP;

  FOR item IN
    SELECT i.indexrelid::regclass AS index_name, c.relname
    FROM pg_index i
    JOIN pg_class c ON c.oid=i.indexrelid
    WHERE i.indrelid IN (
      'testagram_auth.historical_external_staff_roles'::regclass,
      'testagram_auth.historical_external_staff_role_audit'::regclass
    )
      AND c.relname ILIKE '%appwrite%'
      AND NOT EXISTS (SELECT 1 FROM pg_constraint con WHERE con.conindid=i.indexrelid)
  LOOP
    replacement_name := replace(item.relname,'appwrite','source');
    EXECUTE format('ALTER INDEX %s RENAME TO %I',item.index_name,replacement_name);
  END LOOP;
END;
$rename_historical_identifiers$;

REVOKE ALL ON TABLE
  testagram_auth.historical_external_staff_roles,
  testagram_auth.historical_external_staff_role_audit
FROM PUBLIC;

DO $revoke_runtime$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    REVOKE ALL ON TABLE
      testagram_auth.historical_external_staff_roles,
      testagram_auth.historical_external_staff_role_audit
    FROM testagram_app;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
    REVOKE ALL ON TABLE
      testagram_auth.historical_external_staff_roles,
      testagram_auth.historical_external_staff_role_audit
    FROM service_role;
  END IF;
END;
$revoke_runtime$;

COMMENT ON TABLE testagram_auth.historical_external_staff_roles IS
  'Read-restricted historical staff-role assignments. They never authorize native Testagram staff access.';
COMMENT ON TABLE testagram_auth.historical_external_staff_role_audit IS
  'Read-restricted historical staff-role audit evidence. Native staff authorization uses Testagram UUID principals and role grants.';

COMMIT;
