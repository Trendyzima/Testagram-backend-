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
    RAISE EXCEPTION 'both historical and legacy provisioning tables exist; reconcile explicitly';
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

REVOKE ALL ON TABLE
  testagram_identity.historical_external_identity_accounts,
  testagram_identity.historical_external_identity_link_audit,
  testagram_identity.historical_external_registration_consents,
  testagram_identity.historical_external_registration_provisioning
FROM PUBLIC;

DO $revoke_runtime$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    REVOKE ALL ON TABLE
      testagram_identity.historical_external_identity_accounts,
      testagram_identity.historical_external_identity_link_audit,
      testagram_identity.historical_external_registration_consents,
      testagram_identity.historical_external_registration_provisioning
    FROM testagram_app;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
    REVOKE ALL ON TABLE
      testagram_identity.historical_external_identity_accounts,
      testagram_identity.historical_external_identity_link_audit,
      testagram_identity.historical_external_registration_consents,
      testagram_identity.historical_external_registration_provisioning
    FROM service_role;
  END IF;
END;
$revoke_runtime$;

COMMENT ON TABLE testagram_identity.historical_external_identity_accounts IS
  'Read-restricted historical account-link evidence. Not used for runtime authentication or account matching.';
COMMENT ON TABLE testagram_identity.historical_external_identity_link_audit IS
  'Read-restricted historical identity-link audit evidence. Not used for runtime authentication.';
COMMENT ON TABLE testagram_identity.historical_external_registration_consents IS
  'Read-restricted historical registration-consent evidence. Not used as native registration consent or account proof.';
COMMENT ON TABLE testagram_identity.historical_external_registration_provisioning IS
  'Read-restricted historical external-provider provisioning state. Native account provisioning uses native_registration_provisioning.';

COMMIT;
