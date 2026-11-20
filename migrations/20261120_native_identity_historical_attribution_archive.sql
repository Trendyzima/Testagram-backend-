-- Neutralize the remaining provider-specific attribution columns on native
-- identity ledgers. Their values remain intact as historical audit evidence;
-- all new writes use the parallel Testagram UUID columns.
BEGIN;

DO $rename_columns$
BEGIN
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='identity_applications' AND column_name='sensitive_action_request_id')
     AND NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='identity_applications' AND column_name='historical_external_sensitive_action_request_id') THEN
    ALTER TABLE testagram_identity.identity_applications
      RENAME COLUMN sensitive_action_request_id TO historical_external_sensitive_action_request_id;
  ELSIF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='identity_applications' AND column_name='sensitive_action_request_id')
     AND EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='identity_applications' AND column_name='historical_external_sensitive_action_request_id') THEN
    RAISE EXCEPTION 'both historical and legacy identity approval request columns exist';
  END IF;

  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='identity_application_reviews' AND column_name='reviewer_appwrite_user_id')
     AND NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='identity_application_reviews' AND column_name='historical_source_reviewer_id') THEN
    ALTER TABLE testagram_identity.identity_application_reviews
      RENAME COLUMN reviewer_appwrite_user_id TO historical_source_reviewer_id;
  ELSIF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='identity_application_reviews' AND column_name='reviewer_appwrite_user_id')
     AND EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='identity_application_reviews' AND column_name='historical_source_reviewer_id') THEN
    RAISE EXCEPTION 'both historical and provider-keyed identity reviewer columns exist';
  END IF;

  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='verification_reviews' AND column_name='reviewer_appwrite_user_id')
     AND NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='verification_reviews' AND column_name='historical_source_reviewer_id') THEN
    ALTER TABLE testagram_identity.verification_reviews
      RENAME COLUMN reviewer_appwrite_user_id TO historical_source_reviewer_id;
  ELSIF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='verification_reviews' AND column_name='reviewer_appwrite_user_id')
     AND EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='verification_reviews' AND column_name='historical_source_reviewer_id') THEN
    RAISE EXCEPTION 'both historical and provider-keyed verification reviewer columns exist';
  END IF;

  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='verification_sessions' AND column_name='decided_by_appwrite_user_id')
     AND NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='verification_sessions' AND column_name='historical_source_decider_id') THEN
    ALTER TABLE testagram_identity.verification_sessions
      RENAME COLUMN decided_by_appwrite_user_id TO historical_source_decider_id;
  ELSIF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='verification_sessions' AND column_name='decided_by_appwrite_user_id')
     AND EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='verification_sessions' AND column_name='historical_source_decider_id') THEN
    RAISE EXCEPTION 'both historical and provider-keyed verification decider columns exist';
  END IF;

  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='identity_approval_tokens' AND column_name='first_approver_appwrite_user_id')
     AND NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='identity_approval_tokens' AND column_name='historical_first_approver_source_id') THEN
    ALTER TABLE testagram_identity.identity_approval_tokens
      RENAME COLUMN first_approver_appwrite_user_id TO historical_first_approver_source_id;
  ELSIF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='identity_approval_tokens' AND column_name='first_approver_appwrite_user_id')
     AND EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='identity_approval_tokens' AND column_name='historical_first_approver_source_id') THEN
    RAISE EXCEPTION 'both historical and provider-keyed first approver columns exist';
  END IF;

  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='identity_approval_tokens' AND column_name='second_approver_appwrite_user_id')
     AND NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='identity_approval_tokens' AND column_name='historical_second_approver_source_id') THEN
    ALTER TABLE testagram_identity.identity_approval_tokens
      RENAME COLUMN second_approver_appwrite_user_id TO historical_second_approver_source_id;
  ELSIF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='identity_approval_tokens' AND column_name='second_approver_appwrite_user_id')
     AND EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='identity_approval_tokens' AND column_name='historical_second_approver_source_id') THEN
    RAISE EXCEPTION 'both historical and provider-keyed second approver columns exist';
  END IF;

  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='identity_approval_tokens' AND column_name='consumed_by_appwrite_user_id')
     AND NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='identity_approval_tokens' AND column_name='historical_consumed_by_source_id') THEN
    ALTER TABLE testagram_identity.identity_approval_tokens
      RENAME COLUMN consumed_by_appwrite_user_id TO historical_consumed_by_source_id;
  ELSIF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='identity_approval_tokens' AND column_name='consumed_by_appwrite_user_id')
     AND EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='identity_approval_tokens' AND column_name='historical_consumed_by_source_id') THEN
    RAISE EXCEPTION 'both historical and provider-keyed consumer columns exist';
  END IF;
END;
$rename_columns$;

DO $rename_legacy_request_link$
BEGIN
  IF EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid='testagram_identity.identity_applications'::regclass
      AND conname='identity_applications_sensitive_action_request_fk'
  ) AND NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid='testagram_identity.identity_applications'::regclass
      AND conname='identity_applications_historical_external_sensitive_action_request_fk'
  ) THEN
    ALTER TABLE testagram_identity.identity_applications
      RENAME CONSTRAINT identity_applications_sensitive_action_request_fk
      TO identity_applications_historical_external_sensitive_action_request_fk;
  ELSIF EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid='testagram_identity.identity_applications'::regclass
      AND conname='identity_applications_sensitive_action_request_fk'
  ) AND EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid='testagram_identity.identity_applications'::regclass
      AND conname='identity_applications_historical_external_sensitive_action_request_fk'
  ) THEN
    RAISE EXCEPTION 'both legacy and historical identity approval request constraints exist';
  END IF;

  IF to_regclass('testagram_identity.identity_applications_sensitive_action_request_idx') IS NOT NULL THEN
    ALTER INDEX testagram_identity.identity_applications_sensitive_action_request_idx
      RENAME TO historical_external_identity_applications_sensitive_request_idx;
  END IF;
END;
$rename_legacy_request_link$;

-- Column renames update FK and CHECK expressions automatically. Rename any
-- remaining provider-branded constraint identifiers without changing data.
DO $rename_constraints$
DECLARE
  item RECORD;
  replacement_name TEXT;
BEGIN
  FOR item IN
    SELECT conrelid::regclass AS relation_name, conname
    FROM pg_constraint
    WHERE conrelid IN (
      'testagram_identity.identity_application_reviews'::regclass,
      'testagram_identity.verification_reviews'::regclass,
      'testagram_identity.verification_sessions'::regclass,
      'testagram_identity.identity_approval_tokens'::regclass
    )
      AND conname ILIKE '%appwrite%'
  LOOP
    replacement_name := replace(item.conname,'appwrite','source');
    EXECUTE format('ALTER TABLE %s RENAME CONSTRAINT %I TO %I',
      item.relation_name,item.conname,replacement_name);
  END LOOP;
END;
$rename_constraints$;

COMMENT ON COLUMN testagram_identity.identity_applications.native_sensitive_action_request_id IS
  'Testagram-native dual-control approval request for identity account activation.';
COMMENT ON COLUMN testagram_identity.identity_applications.historical_external_sensitive_action_request_id IS
  'Historical external approval request reference retained for audit only; new identity activation uses native_sensitive_action_request_id.';
COMMENT ON COLUMN testagram_identity.identity_application_reviews.reviewer_user_id IS
  'Canonical Testagram staff UUID for new identity-application reviews. Historical source attribution is retained separately.';
COMMENT ON COLUMN testagram_identity.verification_reviews.reviewer_user_id IS
  'Canonical Testagram staff UUID for new verification reviews. Historical source attribution is retained separately.';
COMMENT ON COLUMN testagram_identity.verification_sessions.decided_by_user_id IS
  'Canonical Testagram staff UUID for new verification decisions. Historical source attribution is retained separately.';
COMMENT ON COLUMN testagram_identity.identity_approval_tokens.first_approver_user_id IS
  'Canonical Testagram staff UUID for the first independent account approver.';
COMMENT ON COLUMN testagram_identity.identity_approval_tokens.second_approver_user_id IS
  'Canonical Testagram staff UUID for the second independent account approver.';
COMMENT ON COLUMN testagram_identity.identity_approval_tokens.consumed_by_native_user_id IS
  'Canonical Testagram UUID for the account that redeemed this single-use approval token.';

COMMENT ON COLUMN testagram_identity.identity_application_reviews.historical_source_reviewer_id IS
  'Historical reviewer source identifier retained for audit only; native review authorization uses reviewer_user_id.';
COMMENT ON COLUMN testagram_identity.verification_reviews.historical_source_reviewer_id IS
  'Historical reviewer source identifier retained for audit only; native review authorization uses reviewer_user_id.';
COMMENT ON COLUMN testagram_identity.verification_sessions.historical_source_decider_id IS
  'Historical decision source identifier retained for audit only; native decisions use decided_by_user_id.';
COMMENT ON COLUMN testagram_identity.identity_approval_tokens.historical_first_approver_source_id IS
  'Historical first approver source identifier; native approvals use first_approver_user_id.';
COMMENT ON COLUMN testagram_identity.identity_approval_tokens.historical_second_approver_source_id IS
  'Historical second approver source identifier; native approvals use second_approver_user_id.';
COMMENT ON COLUMN testagram_identity.identity_approval_tokens.historical_consumed_by_source_id IS
  'Historical token consumer source identifier; native consumption uses consumed_by_native_user_id.';

COMMIT;
