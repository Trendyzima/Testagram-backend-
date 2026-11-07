-- Backend-owned federation consent and actor-discovery eligibility.
BEGIN;

ALTER TABLE testagram_local.accounts
  ADD COLUMN IF NOT EXISTS federation_enabled BOOLEAN NOT NULL DEFAULT FALSE;

COMMIT;
