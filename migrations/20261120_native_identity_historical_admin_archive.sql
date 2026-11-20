-- Archive provider-keyed administrative ledgers. Native Admin Console writes
-- only native_sensitive_action_* and native_admin_security_audit tables.
-- Historical foreign keys remain intact after table/column renames.
BEGIN;

DO $rename_tables$
BEGIN
  IF to_regclass('testagram_auth.staff_permission_overrides') IS NOT NULL
     AND to_regclass('testagram_auth.historical_external_staff_permission_overrides') IS NULL THEN
    ALTER TABLE testagram_auth.staff_permission_overrides
      RENAME TO historical_external_staff_permission_overrides;
  ELSIF to_regclass('testagram_auth.staff_permission_overrides') IS NOT NULL
     AND to_regclass('testagram_auth.historical_external_staff_permission_overrides') IS NOT NULL THEN
    RAISE EXCEPTION 'both historical and live legacy staff-permission override tables exist';
  END IF;

  IF to_regclass('testagram_auth.sensitive_action_requests') IS NOT NULL
     AND to_regclass('testagram_auth.historical_external_sensitive_action_requests') IS NULL THEN
    ALTER TABLE testagram_auth.sensitive_action_requests
      RENAME TO historical_external_sensitive_action_requests;
  ELSIF to_regclass('testagram_auth.sensitive_action_requests') IS NOT NULL
     AND to_regclass('testagram_auth.historical_external_sensitive_action_requests') IS NOT NULL THEN
    RAISE EXCEPTION 'both historical and live legacy sensitive-action request tables exist';
  END IF;

  IF to_regclass('testagram_auth.sensitive_action_approvals') IS NOT NULL
     AND to_regclass('testagram_auth.historical_external_sensitive_action_approvals') IS NULL THEN
    ALTER TABLE testagram_auth.sensitive_action_approvals
      RENAME TO historical_external_sensitive_action_approvals;
  ELSIF to_regclass('testagram_auth.sensitive_action_approvals') IS NOT NULL
     AND to_regclass('testagram_auth.historical_external_sensitive_action_approvals') IS NOT NULL THEN
    RAISE EXCEPTION 'both historical and live legacy sensitive-action approval tables exist';
  END IF;

  IF to_regclass('testagram_auth.admin_security_audit') IS NOT NULL
     AND to_regclass('testagram_auth.historical_external_admin_security_audit') IS NULL THEN
    ALTER TABLE testagram_auth.admin_security_audit
      RENAME TO historical_external_admin_security_audit;
  ELSIF to_regclass('testagram_auth.admin_security_audit') IS NOT NULL
     AND to_regclass('testagram_auth.historical_external_admin_security_audit') IS NOT NULL THEN
    RAISE EXCEPTION 'both historical and live legacy admin audit tables exist';
  END IF;
END;
$rename_tables$;

DO $rename_columns$
BEGIN
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_auth' AND table_name='historical_external_staff_permission_overrides' AND column_name='appwrite_user_id') THEN
    ALTER TABLE testagram_auth.historical_external_staff_permission_overrides RENAME COLUMN appwrite_user_id TO source_staff_id;
  END IF;
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_auth' AND table_name='historical_external_staff_permission_overrides' AND column_name='changed_by_appwrite_user_id') THEN
    ALTER TABLE testagram_auth.historical_external_staff_permission_overrides RENAME COLUMN changed_by_appwrite_user_id TO source_changed_by_staff_id;
  END IF;
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_auth' AND table_name='historical_external_sensitive_action_requests' AND column_name='requested_by_appwrite_user_id') THEN
    ALTER TABLE testagram_auth.historical_external_sensitive_action_requests RENAME COLUMN requested_by_appwrite_user_id TO source_requester_staff_id;
  END IF;
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_auth' AND table_name='historical_external_sensitive_action_approvals' AND column_name='approver_appwrite_user_id') THEN
    ALTER TABLE testagram_auth.historical_external_sensitive_action_approvals RENAME COLUMN approver_appwrite_user_id TO source_approver_staff_id;
  END IF;
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_auth' AND table_name='historical_external_admin_security_audit' AND column_name='actor_appwrite_user_id') THEN
    ALTER TABLE testagram_auth.historical_external_admin_security_audit RENAME COLUMN actor_appwrite_user_id TO source_actor_staff_id;
  END IF;
END;
$rename_columns$;

DO $indexes$
BEGIN
  IF to_regclass('testagram_auth.sensitive_action_pending_idx') IS NOT NULL THEN
    ALTER INDEX testagram_auth.sensitive_action_pending_idx RENAME TO historical_external_sensitive_action_pending_idx;
  END IF;
  IF to_regclass('testagram_auth.sensitive_action_approvals_request_idx') IS NOT NULL THEN
    ALTER INDEX testagram_auth.sensitive_action_approvals_request_idx RENAME TO historical_external_sensitive_action_approvals_request_idx;
  END IF;
  IF to_regclass('testagram_auth.admin_security_audit_created_idx') IS NOT NULL THEN
    ALTER INDEX testagram_auth.admin_security_audit_created_idx RENAME TO historical_external_admin_security_audit_created_idx;
  END IF;
  IF to_regclass('testagram_auth.admin_security_audit_actor_idx') IS NOT NULL THEN
    ALTER INDEX testagram_auth.admin_security_audit_actor_idx RENAME TO historical_external_admin_security_audit_actor_idx;
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
      'testagram_auth.historical_external_staff_permission_overrides'::regclass,
      'testagram_auth.historical_external_sensitive_action_requests'::regclass,
      'testagram_auth.historical_external_sensitive_action_approvals'::regclass,
      'testagram_auth.historical_external_admin_security_audit'::regclass
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
      'testagram_auth.historical_external_staff_permission_overrides'::regclass,
      'testagram_auth.historical_external_sensitive_action_requests'::regclass,
      'testagram_auth.historical_external_sensitive_action_approvals'::regclass,
      'testagram_auth.historical_external_admin_security_audit'::regclass
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
  testagram_auth.historical_external_staff_permission_overrides,
  testagram_auth.historical_external_sensitive_action_requests,
  testagram_auth.historical_external_sensitive_action_approvals,
  testagram_auth.historical_external_admin_security_audit
FROM PUBLIC;

DO $revoke_runtime$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    REVOKE ALL ON TABLE
      testagram_auth.historical_external_staff_permission_overrides,
      testagram_auth.historical_external_sensitive_action_requests,
      testagram_auth.historical_external_sensitive_action_approvals,
      testagram_auth.historical_external_admin_security_audit
    FROM testagram_app;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
    REVOKE ALL ON TABLE
      testagram_auth.historical_external_staff_permission_overrides,
      testagram_auth.historical_external_sensitive_action_requests,
      testagram_auth.historical_external_sensitive_action_approvals,
      testagram_auth.historical_external_admin_security_audit
    FROM service_role;
  END IF;
END;
$revoke_runtime$;

COMMENT ON TABLE testagram_auth.historical_external_staff_permission_overrides IS
  'Read-restricted historical provider-keyed permission overrides. Native overrides use native_staff_permission_overrides.';
COMMENT ON TABLE testagram_auth.historical_external_sensitive_action_requests IS
  'Read-restricted historical provider-keyed sensitive-action requests. Native approvals use native_sensitive_action_requests.';
COMMENT ON TABLE testagram_auth.historical_external_sensitive_action_approvals IS
  'Read-restricted historical provider-keyed approval decisions. Native approvals use native_sensitive_action_approvals.';
COMMENT ON TABLE testagram_auth.historical_external_admin_security_audit IS
  'Read-restricted historical provider-keyed admin audit. Native audit uses native_admin_security_audit.';

COMMIT;
