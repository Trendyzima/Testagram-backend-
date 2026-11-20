-- Add the Testagram-native reviewer identity without rewriting historical audit rows.
-- Historical provider IDs remain nullable source evidence; native reviews use staff UUIDs.
BEGIN;

ALTER TABLE testagram_identity.identity_application_reviews
  ADD COLUMN IF NOT EXISTS reviewer_user_id UUID
    REFERENCES testagram_auth.staff_principals(user_id) ON DELETE RESTRICT;

ALTER TABLE testagram_identity.identity_application_reviews
  ALTER COLUMN reviewer_appwrite_user_id DROP NOT NULL;

-- Keep each row attributable to either its historical reviewer key or a native
-- Testagram staff principal. Do not guess mappings for historical provider IDs.
DO $constraint$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM pg_constraint
    WHERE conrelid='testagram_identity.identity_application_reviews'::regclass
      AND conname='identity_application_reviews_reviewer_identity_check'
  ) THEN
    ALTER TABLE testagram_identity.identity_application_reviews
      ADD CONSTRAINT identity_application_reviews_reviewer_identity_check
      CHECK (reviewer_appwrite_user_id IS NOT NULL OR reviewer_user_id IS NOT NULL)
      NOT VALID;
  END IF;
END;
$constraint$;

CREATE UNIQUE INDEX IF NOT EXISTS identity_application_reviews_native_reviewer_uidx
  ON testagram_identity.identity_application_reviews(application_id,reviewer_user_id)
  WHERE reviewer_user_id IS NOT NULL;

COMMENT ON COLUMN testagram_identity.identity_application_reviews.reviewer_user_id IS
  'Testagram staff-principal UUID for native evidence and account reviews.';
COMMENT ON COLUMN testagram_identity.identity_application_reviews.reviewer_appwrite_user_id IS
  'Historical reviewer identifier retained only for audit lineage; never accepted for native authorization.';

COMMIT;
