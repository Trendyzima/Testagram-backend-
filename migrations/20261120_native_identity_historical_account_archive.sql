-- Retire provider-branded identity tables from the live schema without
-- destroying their audit evidence. The original applied migrations remain immutable.
BEGIN;

DO $rename_tables$
BEGIN
  IF to_regclass('testagram_identity.appwrite_accounts') IS NOT NULL
     AND to_regclass('testagram_identity.historical_external_identity_accounts') IS NULL THEN
    ALTER TABLE testagram_identity.appwrite_accounts
      RENAME TO historical_external_identity_accounts;
  ELSIF to_regclass('testagram_identity.appwrite_accounts') IS NOT NULL
     AND to_regclass('testagram_identity.historical_external_identity_accounts') IS NOT NULL THEN
    RAISE EXCEPTION 'both historical and legacy identity account tables exist; reconcile explicitly';
  END IF;

  IF to_regclass('testagram_identity.link_audit') IS NOT NULL
     AND to_regclass('testagram_identity.historical_external_identity_link_audit') IS NULL THEN
    ALTER TABLE testagram_identity.link_audit
      RENAME TO historical_external_identity_link_audit;
  ELSIF to_regclass('testagram_identity.link_audit') IS NOT NULL
     AND to_regclass('testagram_identity.historical_external_identity_link_audit') IS NOT NULL THEN
    RAISE EXCEPTION 'both historical and legacy identity link-audit tables exist; reconcile explicitly';
  END IF;

  IF to_regclass('testagram_identity.appwrite_registration_consents') IS NOT NULL
     AND to_regclass('testagram_identity.historical_external_registration_consents') IS NULL THEN
    ALTER TABLE testagram_identity.appwrite_registration_consents
      RENAME TO historical_external_registration_consents;
  ELSIF to_regclass('testagram_identity.appwrite_registration_consents') IS NOT NULL
     AND to_regclass('testagram_identity.historical_external_registration_consents') IS NOT NULL THEN
    RAISE EXCEPTION 'both historical and legacy consent tables exist; reconcile explicitly';
  END IF;

  IF to_regclass('testagram_identity.registration_provisioning') IS NOT NULL
     AND to_regclass('testagram_identity.historical_external_registration_provisioning') IS NULL THEN
    ALTER TABLE testagram_identity.registration_provisioning
      RENAME TO historical_external_registration_provisioning;
  ELSIF to_regclass('testagram_identity.registration_provisioning') IS NOT NULL
     AND to_regclass('testagram_identity.historical_external_registration_provisioning') IS NOT NULL THEN
    -- Preserve both ledgers separately. The established archive name remains
    -- stable; the second provider-keyed copy is retained under a distinct,
    -- restricted historical name. Never merge, overwrite, or discard rows.
    IF to_regclass('testagram_identity.historical_external_registration_provisioning_recovered') IS NOT NULL THEN
      RAISE EXCEPTION 'a recovered historical provisioning archive already exists; inspect the new legacy ledger before proceeding';
    END IF;
    ALTER TABLE testagram_identity.registration_provisioning
      RENAME TO historical_external_registration_provisioning_recovered;
  END IF;
END;
$rename_tables$;

DO $rename_columns$
BEGIN
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='historical_external_identity_accounts' AND column_name='appwrite_user_id') THEN
    ALTER TABLE testagram_identity.historical_external_identity_accounts RENAME COLUMN appwrite_user_id TO source_account_id;
  END IF;
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='historical_external_identity_link_audit' AND column_name='appwrite_user_id') THEN
    ALTER TABLE testagram_identity.historical_external_identity_link_audit RENAME COLUMN appwrite_user_id TO source_account_id;
  END IF;
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='historical_external_registration_consents' AND column_name='appwrite_user_id') THEN
    ALTER TABLE testagram_identity.historical_external_registration_consents RENAME COLUMN appwrite_user_id TO source_account_id;
  END IF;
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='historical_external_registration_provisioning' AND column_name='appwrite_user_id') THEN
    ALTER TABLE testagram_identity.historical_external_registration_provisioning RENAME COLUMN appwrite_user_id TO source_account_id;
  END IF;
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='historical_external_registration_provisioning_recovered' AND column_name='appwrite_user_id') THEN
    ALTER TABLE testagram_identity.historical_external_registration_provisioning_recovered RENAME COLUMN appwrite_user_id TO source_account_id;
  END IF;
END;
$rename_columns$;

DO $indexes$
BEGIN
  IF to_regclass('testagram_identity.appwrite_accounts_email_idx') IS NOT NULL THEN
    ALTER INDEX testagram_identity.appwrite_accounts_email_idx RENAME TO historical_external_identity_accounts_email_idx;
  END IF;
  IF to_regclass('testagram_identity.appwrite_accounts_legacy_user_idx') IS NOT NULL THEN
    ALTER INDEX testagram_identity.appwrite_accounts_legacy_user_idx RENAME TO historical_external_identity_accounts_legacy_user_idx;
  END IF;
  IF to_regclass('testagram_identity.appwrite_identity_link_audit_canonical_created') IS NOT NULL THEN
    ALTER INDEX testagram_identity.appwrite_identity_link_audit_canonical_created RENAME TO historical_external_identity_link_audit_canonical_created;
  END IF;
  IF to_regclass('testagram_identity.appwrite_identity_link_audit_appwrite_created') IS NOT NULL THEN
    ALTER INDEX testagram_identity.appwrite_identity_link_audit_appwrite_created RENAME TO historical_external_identity_link_audit_source_created;
  END IF;
  IF to_regclass('testagram_identity.appwrite_registration_consents_user_idx') IS NOT NULL THEN
    ALTER INDEX testagram_identity.appwrite_registration_consents_user_idx RENAME TO historical_external_registration_consents_source_user_idx;
  END IF;
END;
$indexes$;

-- Constraint names and standalone index names are catalog objects too. Rename
-- any provider-branded identifiers left on archived relations without touching
-- their definitions or historical row values.
DO $rename_historical_identifiers$
DECLARE
  item RECORD;
  replacement_name TEXT;
BEGIN
  FOR item IN
    SELECT conrelid::regclass AS relation_name, conname
    FROM pg_constraint
    WHERE conrelid IN (
      to_regclass('testagram_identity.historical_external_identity_accounts'),
      to_regclass('testagram_identity.historical_external_identity_link_audit'),
      to_regclass('testagram_identity.historical_external_registration_consents'),
      to_regclass('testagram_identity.historical_external_registration_provisioning'),
      to_regclass('testagram_identity.historical_external_registration_provisioning_recovered')
    )
      AND conname ILIKE '%appwrite%'
  LOOP
    replacement_name := replace(item.conname,'appwrite','source');
    -- A second recovered ledger can coexist with the established archive.
    -- Constraint-backed unique indexes are schema-wide names, so neutralizing
    -- both tables to the same name would collide. Give recovered objects a
    -- deterministic, PostgreSQL-safe suffix while preserving their semantics.
    IF right(item.relation_name::text,10) = '_recovered' THEN
      replacement_name := left(replacement_name,44) || '_recovered_' ||
        substr(md5(item.relation_name::text || ':' || item.conname),1,8);
    END IF;
    EXECUTE format('ALTER TABLE %s RENAME CONSTRAINT %I TO %I',
      item.relation_name,item.conname,replacement_name);
  END LOOP;

  FOR item IN
    SELECT i.indrelid::regclass AS relation_name,
           i.indexrelid::regclass AS index_name,
           c.relname
    FROM pg_index i
    JOIN pg_class c ON c.oid=i.indexrelid
    WHERE i.indrelid IN (
      to_regclass('testagram_identity.historical_external_identity_accounts'),
      to_regclass('testagram_identity.historical_external_identity_link_audit'),
      to_regclass('testagram_identity.historical_external_registration_consents'),
      to_regclass('testagram_identity.historical_external_registration_provisioning'),
      to_regclass('testagram_identity.historical_external_registration_provisioning_recovered')
    )
      AND c.relname ILIKE '%appwrite%'
      AND NOT EXISTS (SELECT 1 FROM pg_constraint con WHERE con.conindid=i.indexrelid)
  LOOP
    replacement_name := replace(item.relname,'appwrite','source');
    IF right(item.relation_name::text,10) = '_recovered' THEN
      replacement_name := left(replacement_name,44) || '_recovered_' ||
        substr(md5(item.relation_name::text || ':' || item.relname),1,8);
    END IF;
    EXECUTE format('ALTER INDEX %s RENAME TO %I',item.index_name,replacement_name);
  END LOOP;
END;
$rename_historical_identifiers$;

-- The retired saga status was provider-specific. Preserve the event meaning
-- while neutralizing the label in the restricted historical ledger.
DO $neutralize_historical_status$
DECLARE
  relation_name TEXT;
  constraint_name TEXT;
  relation_names TEXT[] := ARRAY[
    'testagram_identity.historical_external_registration_provisioning',
    'testagram_identity.historical_external_registration_provisioning_recovered'
  ];
  status_constraint_name TEXT;
BEGIN
  FOREACH relation_name IN ARRAY relation_names LOOP
    IF to_regclass(relation_name) IS NOT NULL THEN
      FOR constraint_name IN
        SELECT conname FROM pg_constraint
        WHERE conrelid=to_regclass(relation_name)
          AND contype='c'
          AND pg_get_constraintdef(oid) LIKE '%appwrite_created%'
      LOOP
        EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I',relation_name,constraint_name);
      END LOOP;

      EXECUTE format(
        'UPDATE %s SET status=''source_provider_created'' WHERE status=''appwrite_created''',
        relation_name
      );

      status_constraint_name := CASE
        WHEN relation_name LIKE '%_recovered'
          THEN 'historical_external_reg_provisioning_recovered_status_check'
        ELSE 'historical_external_registration_provisioning_status_check'
      END;
      IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid=to_regclass(relation_name)
          AND conname=status_constraint_name
      ) THEN
        EXECUTE format(
          'ALTER TABLE %s ADD CONSTRAINT %I CHECK (status IN (''pending'',''source_provider_created'',''linked'',''verification_sent'',''completed'',''compensation_required'',''failed''))',
          relation_name,status_constraint_name
        );
      END IF;
    END IF;
  END LOOP;
END;
$neutralize_historical_status$;

-- Normalize the provenance label in the native reconciliation ledger while
-- preserving the source account ID and all reconciliation evidence.
ALTER TABLE testagram_auth.legacy_account_reconciliation
  DROP CONSTRAINT IF EXISTS legacy_account_reconciliation_source_provider_check;
UPDATE testagram_auth.legacy_account_reconciliation
  SET source_provider='historical_external_provider'
  WHERE source_provider='appwrite';
ALTER TABLE testagram_auth.legacy_account_reconciliation
  ADD CONSTRAINT legacy_account_reconciliation_source_provider_check
  CHECK (source_provider='historical_external_provider');

DO $lock_historical_identity_tables$
DECLARE
  relation_name TEXT;
  relation_names TEXT[] := ARRAY[
    'testagram_identity.historical_external_identity_accounts',
    'testagram_identity.historical_external_identity_link_audit',
    'testagram_identity.historical_external_registration_consents',
    'testagram_identity.historical_external_registration_provisioning',
    'testagram_identity.historical_external_registration_provisioning_recovered'
  ];
BEGIN
  FOREACH relation_name IN ARRAY relation_names LOOP
    IF to_regclass(relation_name) IS NOT NULL THEN
      EXECUTE format('REVOKE ALL ON TABLE %s FROM PUBLIC',relation_name);
      IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
        EXECUTE format('REVOKE ALL ON TABLE %s FROM testagram_app',relation_name);
      END IF;
      IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
        EXECUTE format('REVOKE ALL ON TABLE %s FROM service_role',relation_name);
      END IF;
    END IF;
  END LOOP;
END;
$lock_historical_identity_tables$;

DO $comment_historical_identity_tables$
BEGIN
  IF to_regclass('testagram_identity.historical_external_identity_accounts') IS NOT NULL THEN
    COMMENT ON TABLE testagram_identity.historical_external_identity_accounts IS
      'Read-restricted historical account-link evidence. Not used for runtime authentication or account matching.';
  END IF;
  IF to_regclass('testagram_identity.historical_external_identity_link_audit') IS NOT NULL THEN
    COMMENT ON TABLE testagram_identity.historical_external_identity_link_audit IS
      'Read-restricted historical identity-link audit evidence. Not used for runtime authentication.';
  END IF;
  IF to_regclass('testagram_identity.historical_external_registration_consents') IS NOT NULL THEN
    COMMENT ON TABLE testagram_identity.historical_external_registration_consents IS
      'Read-restricted historical registration-consent evidence. Not used as native registration consent or account proof.';
  END IF;
  IF to_regclass('testagram_identity.historical_external_registration_provisioning') IS NOT NULL THEN
    COMMENT ON TABLE testagram_identity.historical_external_registration_provisioning IS
      'Read-restricted historical external-provider provisioning state. Native account provisioning uses native_registration_provisioning.';
  END IF;
  IF to_regclass('testagram_identity.historical_external_registration_provisioning_recovered') IS NOT NULL THEN
    COMMENT ON TABLE testagram_identity.historical_external_registration_provisioning_recovered IS
      'Additional read-restricted historical provisioning ledger retained separately after a duplicate legacy table was found. Not used by runtime authentication.';
  END IF;
END;
$comment_historical_identity_tables$;

COMMIT;
