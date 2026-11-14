-- Append-only consent evidence for self-service Appwrite registration.
-- This is separate from the high-assurance manual-review application ledger:
-- ordinary registration requires age/Terms/Privacy declarations, not two-person
-- identity review. Appwrite remains the credential authority.
BEGIN;

CREATE TABLE IF NOT EXISTS testagram_identity.appwrite_registration_consents (
  id BIGSERIAL PRIMARY KEY,
  appwrite_user_id TEXT NOT NULL CHECK (length(appwrite_user_id) BETWEEN 1 AND 36),
  email TEXT NOT NULL CHECK (length(email) BETWEEN 3 AND 254 AND email = lower(btrim(email))),
  consent_type TEXT NOT NULL CHECK (consent_type IN ('terms','privacy','age_confirmation')),
  policy_version TEXT NOT NULL CHECK (length(btrim(policy_version)) BETWEEN 1 AND 64),
  accepted_at TIMESTAMPTZ NOT NULL,
  request_id TEXT NOT NULL DEFAULT '' CHECK (length(request_id) <= 128),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(appwrite_user_id, consent_type, policy_version, email)
);

CREATE INDEX IF NOT EXISTS appwrite_registration_consents_user_idx
  ON testagram_identity.appwrite_registration_consents(appwrite_user_id, email, consent_type);

REVOKE ALL ON TABLE testagram_identity.appwrite_registration_consents FROM PUBLIC;

DO $grants$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    GRANT SELECT, INSERT ON testagram_identity.appwrite_registration_consents TO testagram_app;
    GRANT USAGE, SELECT ON SEQUENCE testagram_identity.appwrite_registration_consents_id_seq TO testagram_app;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
    GRANT USAGE ON SCHEMA testagram_identity TO service_role;
    GRANT SELECT, INSERT ON testagram_identity.appwrite_registration_consents TO service_role;
    GRANT USAGE, SELECT ON SEQUENCE testagram_identity.appwrite_registration_consents_id_seq TO service_role;
  END IF;
END;
$grants$;

COMMENT ON TABLE testagram_identity.appwrite_registration_consents IS
  'Append-only age self-declaration and versioned Terms/Privacy consent for self-service Appwrite accounts. No passwords, JWTs or identity-document data are stored.';

COMMIT;
