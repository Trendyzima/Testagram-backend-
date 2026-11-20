-- ARCHIVED ONE-TIME LEGACY IDENTITY IMPORT; NOT A RUNTIME DEPENDENCY.
-- Use only for an approved, evidence-backed reconciliation of historical provider accounts.
-- This script must never be called by the API, CI deployment or normal startup.
--
-- This script DOES NOT export/import source-provider passwords (they are not
-- recoverable). It uses only the explicit source_account_id -> canonical UUID
-- mapping already recorded in testagram_identity.historical_external_identity_accounts.
--
-- Run only after a reviewed account reconciliation export and verified DB
-- backup. Supply:
--   psql -v ON_ERROR_STOP=1 \
--     -v reconciled_by='operator-id' \
--     -v evidence_reference='change-ticket-or-signed-export-reference' \
--     -f scripts/archive/legacy-identity-provider-import.sql
--
-- Every imported account receives a non-verifiable sentinel password hash and
-- password_reset_required=true. Verified users must complete the native
-- password-reset flow before native login. Unverified users remain unverified.
-- This historical import does not enable or require the former identity provider at runtime.

\if :{?reconciled_by}
\else
  \echo 'ERROR: -v reconciled_by is required'
  \quit 3
\endif
\if :{?evidence_reference}
\else
  \echo 'ERROR: -v evidence_reference is required'
  \quit 3
\endif

BEGIN;
SELECT set_config('testagram.migration_operator', :'reconciled_by', true);
SELECT set_config('testagram.migration_evidence', :'evidence_reference', true);

DO $preflight$
DECLARE
  n BIGINT;
BEGIN
  IF length(btrim(current_setting('testagram.migration_operator'))) NOT BETWEEN 1 AND 128 THEN
    RAISE EXCEPTION 'reconciled_by must be 1..128 characters';
  END IF;
  IF length(btrim(current_setting('testagram.migration_evidence'))) NOT BETWEEN 8 AND 512 THEN
    RAISE EXCEPTION 'evidence_reference must be 8..512 characters';
  END IF;

  SELECT count(*) INTO n
  FROM testagram_identity.historical_external_identity_accounts a
  LEFT JOIN testagram_local.accounts u ON u.user_id=a.canonical_user_id
  WHERE u.user_id IS NULL;
  IF n > 0 THEN RAISE EXCEPTION '% historical source mappings have no canonical Testagram account; quarantine and reconcile first', n; END IF;

  SELECT count(*) INTO n FROM (
    SELECT lower(btrim(a.email)) FROM testagram_identity.historical_external_identity_accounts a
    JOIN testagram_local.accounts u ON u.user_id=a.canonical_user_id
    WHERE a.email IS NOT NULL
    GROUP BY lower(btrim(a.email)) HAVING count(*) > 1
  ) collisions;
  IF n > 0 THEN RAISE EXCEPTION '% duplicate source email groups found; do not auto-link by email', n; END IF;

  SELECT count(*) INTO n FROM (
    SELECT lower(btrim(u.username)) FROM testagram_identity.historical_external_identity_accounts a
    JOIN testagram_local.accounts u ON u.user_id=a.canonical_user_id
    GROUP BY lower(btrim(u.username)) HAVING count(*) > 1
  ) collisions;
  IF n > 0 THEN RAISE EXCEPTION '% duplicate canonical username groups found', n; END IF;

  SELECT count(*) INTO n
  FROM testagram_identity.historical_external_identity_accounts a
  JOIN testagram_local.accounts u ON u.user_id=a.canonical_user_id
  WHERE a.email IS NULL OR length(btrim(a.email)) < 3
     OR u.username !~ '^[A-Za-z0-9_]{3,30}$';
  IF n > 0 THEN RAISE EXCEPTION '% mapped accounts have invalid email or username; quarantine and resolve first', n; END IF;

  SELECT count(*) INTO n
  FROM testagram_identity.historical_external_identity_accounts a
  JOIN testagram_local.accounts u ON u.user_id=a.canonical_user_id
  JOIN testagram_auth.identities i
    ON i.user_id=a.canonical_user_id
    OR i.email_normalized=lower(btrim(a.email))
    OR lower(i.username)=lower(u.username);
  IF n > 0 THEN RAISE EXCEPTION '% canonical ID/email/username conflicts with existing native identity rows', n; END IF;

  SELECT count(*) INTO n
  FROM testagram_identity.historical_external_identity_accounts a
  JOIN testagram_auth.legacy_account_reconciliation l
    ON l.source_provider='historical_external_provider'
   AND (l.source_account_id=a.source_account_id OR l.canonical_user_id=a.canonical_user_id);
  IF n > 0 THEN RAISE EXCEPTION '% source or canonical accounts were already reconciled; inspect ledger instead of rerunning', n; END IF;

  IF EXISTS (
    SELECT 1 FROM testagram_identity.historical_external_identity_accounts a
    JOIN testagram_local.accounts u ON u.user_id=a.canonical_user_id
    WHERE a.email <> lower(btrim(a.email))
  ) THEN
    RAISE EXCEPTION 'source email normalization mismatch; reconcile explicitly before import';
  END IF;
END;
$preflight$;

-- Preserve canonical UUIDs exactly. No source email match is used to discover
-- the canonical user: the pre-existing historical source mapping is the authority.
-- The historical source exposes only a verified boolean here, not the original verification
-- timestamp; use migration time rather than misrepresenting last_seen_at.
INSERT INTO testagram_auth.identities
  (user_id,email,username,password_hash,email_verified_at,password_reset_required,disabled_at)
SELECT
  a.canonical_user_id,
  a.email,
  u.username,
  '!testagram-password-reset-required!',
  CASE WHEN a.email_verified THEN now() ELSE NULL END,
  TRUE,
  CASE WHEN u.account_status='active' THEN NULL ELSE now() END
FROM testagram_identity.historical_external_identity_accounts a
JOIN testagram_local.accounts u ON u.user_id=a.canonical_user_id;

INSERT INTO testagram_auth.legacy_account_reconciliation
  (source_provider,source_account_id,canonical_user_id,source_email,
   source_email_verified,source_username,reconciled_by,evidence_reference)
SELECT
  'historical_external_provider',a.source_account_id,a.canonical_user_id,a.email,
  a.email_verified,u.username,
  current_setting('testagram.migration_operator'),
  current_setting('testagram.migration_evidence')
FROM testagram_identity.historical_external_identity_accounts a
JOIN testagram_local.accounts u ON u.user_id=a.canonical_user_id;

INSERT INTO testagram_auth.security_audit(subject_user_id,event_type,outcome,metadata)
SELECT
  a.canonical_user_id,
  'auth.legacy_imported',
  'success',
  jsonb_build_object(
    'source_provider','historical_external_provider',
    'source_account_id',a.source_account_id,
    'credential_state','password_reset_required',
    'email_verified',a.email_verified
  )
FROM testagram_identity.historical_external_identity_accounts a
JOIN testagram_local.accounts u ON u.user_id=a.canonical_user_id;

COMMIT;
