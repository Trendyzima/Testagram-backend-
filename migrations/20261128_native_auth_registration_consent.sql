-- Native self-service account legal consent.
-- Versioned Terms/Privacy acceptance and age self-declaration; no government
-- identity/document/evidence fields are collected or required.
BEGIN;

CREATE TABLE IF NOT EXISTS testagram_auth.registration_consents (
  id BIGSERIAL PRIMARY KEY,
  user_id UUID NOT NULL REFERENCES testagram_auth.identities(user_id) ON DELETE RESTRICT,
  email TEXT NOT NULL CHECK (length(email) BETWEEN 3 AND 254 AND email = lower(btrim(email))),
  consent_type TEXT NOT NULL CHECK (consent_type IN ('terms','privacy','age_confirmation')),
  policy_version TEXT NOT NULL CHECK (length(btrim(policy_version)) BETWEEN 1 AND 64),
  accepted_at TIMESTAMPTZ NOT NULL,
  request_id TEXT NOT NULL DEFAULT '' CHECK (length(request_id) <= 128),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(user_id, consent_type, policy_version)
);

CREATE INDEX IF NOT EXISTS testagram_auth_registration_consents_user_idx
  ON testagram_auth.registration_consents(user_id, consent_type, accepted_at DESC);

REVOKE ALL ON TABLE testagram_auth.registration_consents FROM PUBLIC;
DO $grants$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    GRANT SELECT, INSERT ON testagram_auth.registration_consents TO testagram_app;
    GRANT USAGE, SELECT ON SEQUENCE testagram_auth.registration_consents_id_seq TO testagram_app;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
    GRANT USAGE ON SCHEMA testagram_auth TO service_role;
    GRANT SELECT, INSERT ON testagram_auth.registration_consents TO service_role;
    GRANT USAGE, SELECT ON SEQUENCE testagram_auth.registration_consents_id_seq TO service_role;
  END IF;
END;
$grants$;

COMMENT ON TABLE testagram_auth.registration_consents IS
  'Append-only versioned Terms/Privacy acceptance and age self-declaration for native Testagram signup; no government identity evidence is collected.';

COMMIT;
