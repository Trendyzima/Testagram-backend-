-- Durable, server-verified consent for ordinary Appwrite self-service registration.
-- The Appwrite session identifies the account; the Backend records server time,
-- server policy versions and the email returned by Appwrite (never client claims).
BEGIN;

CREATE TABLE IF NOT EXISTS testagram_identity.appwrite_registration_consents (
  id BIGSERIAL PRIMARY KEY,
  appwrite_user_id TEXT NOT NULL
    CHECK (length(btrim(appwrite_user_id)) BETWEEN 1 AND 36),
  email TEXT NOT NULL
    CHECK (length(email) BETWEEN 3 AND 254 AND email = lower(btrim(email))),
  consent_type TEXT NOT NULL
    CHECK (consent_type IN ('age_confirmation','terms','privacy')),
  policy_version TEXT NOT NULL
    CHECK (length(btrim(policy_version)) BETWEEN 1 AND 64),
  accepted_at TIMESTAMPTZ NOT NULL,
  request_id TEXT NOT NULL DEFAULT ''
    CHECK (length(request_id) <= 128),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (appwrite_user_id, consent_type, policy_version)
);

CREATE INDEX IF NOT EXISTS appwrite_registration_consents_user_idx
  ON testagram_identity.appwrite_registration_consents(appwrite_user_id, email);

REVOKE ALL ON TABLE testagram_identity.appwrite_registration_consents FROM PUBLIC;

DO $grants$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'testagram_app') THEN
    GRANT SELECT, INSERT ON testagram_identity.appwrite_registration_consents TO testagram_app;
    GRANT USAGE, SELECT ON SEQUENCE testagram_identity.appwrite_registration_consents_id_seq TO testagram_app;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'service_role') THEN
    GRANT USAGE ON SCHEMA testagram_identity TO service_role;
    GRANT SELECT, INSERT ON testagram_identity.appwrite_registration_consents TO service_role;
    GRANT USAGE, SELECT ON SEQUENCE testagram_identity.appwrite_registration_consents_id_seq TO service_role;
  END IF;
END;
$grants$;

COMMENT ON TABLE testagram_identity.appwrite_registration_consents IS
  'Append-only age, Terms and Privacy consent for Appwrite self-service registration. Appwrite JWT identity and account email are verified by the Backend before inserts.';

COMMIT;
