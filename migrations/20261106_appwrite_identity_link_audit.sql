-- Audit successful Appwrite-to-legacy identity links in the same transaction
-- as the mapping. This migration is additive and safe to apply to an existing DB.
BEGIN;

CREATE TABLE IF NOT EXISTS testagram_identity.link_audit (
  audit_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  appwrite_user_id TEXT NOT NULL,
  canonical_user_id UUID NOT NULL,
  legacy_user_id UUID NOT NULL,
  event_type TEXT NOT NULL CHECK (event_type IN ('linked','already_linked')),
  request_id TEXT NOT NULL DEFAULT '' CHECK (length(request_id) <= 128),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS appwrite_identity_link_audit_canonical_created
  ON testagram_identity.link_audit(canonical_user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS appwrite_identity_link_audit_appwrite_created
  ON testagram_identity.link_audit(appwrite_user_id, created_at DESC);

REVOKE ALL ON TABLE testagram_identity.link_audit FROM PUBLIC;

DO $grants$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    GRANT INSERT, SELECT ON testagram_identity.link_audit TO testagram_app;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
    GRANT INSERT, SELECT ON testagram_identity.link_audit TO service_role;
  END IF;
END;
$grants$;

COMMENT ON TABLE testagram_identity.link_audit IS
  'Append-only audit history for verified Appwrite-to-legacy Testagram account linking.';

COMMIT;
