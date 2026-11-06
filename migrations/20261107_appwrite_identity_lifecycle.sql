-- Fresh Appwrite identity lifecycle controls.
-- Existing users are protected by a complete legacy-email inventory before
-- new Appwrite identities may be provisioned. The default is intentionally closed.
BEGIN;
CREATE TABLE IF NOT EXISTS testagram_identity.migration_control (
  singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
  legacy_inventory_complete BOOLEAN NOT NULL DEFAULT FALSE,
  legacy_inventory_completed_at TIMESTAMPTZ,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO testagram_identity.migration_control(singleton, legacy_inventory_complete)
VALUES (TRUE, FALSE)
ON CONFLICT (singleton) DO NOTHING;

CREATE TABLE IF NOT EXISTS testagram_identity.legacy_email_reservations (
  email_normalized TEXT PRIMARY KEY CHECK (
    length(email_normalized) BETWEEN 3 AND 254
    AND email_normalized = lower(btrim(email_normalized))
  ),
  legacy_user_id UUID NOT NULL UNIQUE,
  canonical_user_id UUID,
  imported_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS legacy_email_reservations_canonical
  ON testagram_identity.legacy_email_reservations(canonical_user_id)
  WHERE canonical_user_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS testagram_identity.legal_acceptances (
  canonical_user_id UUID PRIMARY KEY REFERENCES testagram_local.accounts(user_id) ON DELETE RESTRICT,
  birth_date DATE NOT NULL,
  terms_accepted BOOLEAN NOT NULL CHECK (terms_accepted),
  privacy_accepted BOOLEAN NOT NULL CHECK (privacy_accepted),
  content_policy_accepted BOOLEAN NOT NULL CHECK (content_policy_accepted),
  age_confirmed BOOLEAN NOT NULL CHECK (age_confirmed),
  policy_version TEXT NOT NULL CHECK (length(policy_version) BETWEEN 1 AND 32),
  accepted_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
REVOKE ALL ON TABLE testagram_identity.legal_acceptances FROM PUBLIC;
CREATE TABLE IF NOT EXISTS testagram_identity.verification_approvals (
  appwrite_user_id TEXT PRIMARY KEY CHECK (length(appwrite_user_id) BETWEEN 1 AND 36),
  registration_id TEXT NOT NULL UNIQUE CHECK (length(registration_id) BETWEEN 1 AND 128),
  provider_event_id TEXT NOT NULL UNIQUE CHECK (length(provider_event_id) BETWEEN 1 AND 128),
  approved_at TIMESTAMPTZ NOT NULL,
  ingested_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
REVOKE ALL ON TABLE testagram_identity.verification_approvals FROM PUBLIC;
CREATE TABLE IF NOT EXISTS testagram_identity.identity_events (
  event_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  appwrite_user_id TEXT NOT NULL,
  canonical_user_id UUID NOT NULL,
  event_type TEXT NOT NULL CHECK (event_type IN ('provisioned','session_resolved','legacy_linked')),
  request_id TEXT NOT NULL DEFAULT '' CHECK (length(request_id) <= 128),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS identity_events_canonical_created
  ON testagram_identity.identity_events(canonical_user_id, created_at DESC);

REVOKE ALL ON TABLE testagram_identity.migration_control,
  testagram_identity.legacy_email_reservations,
  testagram_identity.identity_events FROM PUBLIC;
DO $grants$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    GRANT SELECT ON testagram_identity.migration_control,
      testagram_identity.legacy_email_reservations,
      testagram_identity.verification_approvals TO testagram_app;
    GRANT INSERT ON testagram_identity.verification_approvals TO testagram_app;
    GRANT INSERT, SELECT ON testagram_identity.identity_events, testagram_identity.legal_acceptances TO testagram_app;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
    GRANT SELECT ON testagram_identity.migration_control,
      testagram_identity.legacy_email_reservations TO service_role;
    GRANT INSERT, SELECT ON testagram_identity.identity_events, testagram_identity.legal_acceptances, testagram_identity.verification_approvals TO service_role;
  END IF;
END;
$grants$;
COMMENT ON TABLE testagram_identity.legacy_email_reservations IS
  'Imported inventory of legacy accounts. Email match reserves an address but is never proof of account ownership.';
COMMENT ON TABLE testagram_identity.migration_control IS
  'New Appwrite account provisioning stays disabled until a complete legacy account/email inventory has been imported and verified.';
COMMIT;
