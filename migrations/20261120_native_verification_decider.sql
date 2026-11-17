-- Native Testagram reviewer attribution for verification-session decisions.
-- Preserve historical provider attribution, but all new native reviewer decisions
-- are recorded against canonical staff UUIDs.
BEGIN;

ALTER TABLE testagram_identity.verification_sessions
  ADD COLUMN IF NOT EXISTS decided_by_user_id UUID
    REFERENCES testagram_auth.staff_principals(user_id) ON DELETE RESTRICT;

-- The original check coupled decided_at exclusively to the legacy provider ID.
-- Replace it with an exactly-one-attribution invariant supporting either
-- historical Appwrite decisions or new native Testagram decisions.
DO $decision_attribution$
DECLARE
  constraint_name text;
BEGIN
  FOR constraint_name IN
    SELECT conname
    FROM pg_constraint
    WHERE conrelid='testagram_identity.verification_sessions'::regclass
      AND contype='c'
      AND pg_get_constraintdef(oid) LIKE '%decided_by_appwrite_user_id%'
      AND pg_get_constraintdef(oid) LIKE '%decided_at%'
      AND conname <> 'verification_sessions_decision_attribution_check'
  LOOP
    EXECUTE format(
      'ALTER TABLE testagram_identity.verification_sessions DROP CONSTRAINT %I',
      constraint_name
    );
  END LOOP;

  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname='verification_sessions_decision_attribution_check'
      AND conrelid='testagram_identity.verification_sessions'::regclass
  ) THEN
    ALTER TABLE testagram_identity.verification_sessions
      ADD CONSTRAINT verification_sessions_decision_attribution_check
      CHECK (
        (
          decided_at IS NULL
          AND decided_by_appwrite_user_id IS NULL
          AND decided_by_user_id IS NULL
        )
        OR
        (
          decided_at IS NOT NULL
          AND (
            (decided_by_appwrite_user_id IS NOT NULL AND decided_by_user_id IS NULL)
            OR
            (decided_by_appwrite_user_id IS NULL AND decided_by_user_id IS NOT NULL)
          )
        )
      ) NOT VALID;
  END IF;
END;
$decision_attribution$;

CREATE INDEX IF NOT EXISTS verification_sessions_native_decider_idx
  ON testagram_identity.verification_sessions(decided_by_user_id,decided_at DESC)
  WHERE decided_by_user_id IS NOT NULL;

COMMENT ON COLUMN testagram_identity.verification_sessions.decided_by_user_id IS
  'Canonical Testagram staff UUID for new verification decisions. Legacy Appwrite attribution remains in decided_by_appwrite_user_id for historical records only.';

COMMIT;
