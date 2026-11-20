-- Native Testagram staff WebAuthn credentials and one-time ceremonies.
-- Credentials are bound to canonical staff principal UUIDs, never email or Console-local IDs.
BEGIN;

CREATE TABLE IF NOT EXISTS testagram_auth.staff_webauthn_users (
  rpid varchar(512) NOT NULL,
  user_id uuid NOT NULL REFERENCES testagram_auth.staff_principals(user_id) ON DELETE CASCADE,
  user_handle bytea NOT NULL CHECK (octet_length(user_handle) BETWEEN 16 AND 64),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (rpid,user_id),
  UNIQUE (rpid,user_handle)
);

CREATE TABLE IF NOT EXISTS testagram_auth.staff_webauthn_credentials (
  id bigserial PRIMARY KEY,
  rpid varchar(512) NOT NULL,
  user_id uuid NOT NULL REFERENCES testagram_auth.staff_principals(user_id) ON DELETE CASCADE,
  credential_id bytea NOT NULL,
  credential jsonb NOT NULL,
  sign_count bigint NOT NULL DEFAULT 0 CHECK (sign_count >= 0),
  clone_warning boolean NOT NULL DEFAULT false,
  user_verified boolean NOT NULL DEFAULT false,
  backup_eligible boolean NOT NULL DEFAULT false,
  backup_state boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  last_used_at timestamptz,
  revoked_at timestamptz,
  UNIQUE (rpid,credential_id),
  FOREIGN KEY (rpid,user_id) REFERENCES testagram_auth.staff_webauthn_users(rpid,user_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS staff_webauthn_credentials_user_idx
  ON testagram_auth.staff_webauthn_credentials(rpid,user_id) WHERE revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS testagram_auth.staff_webauthn_challenges (
  challenge_hash bytea PRIMARY KEY CHECK (octet_length(challenge_hash)=32),
  user_id uuid NOT NULL REFERENCES testagram_auth.staff_principals(user_id) ON DELETE CASCADE,
  ceremony text NOT NULL CHECK (ceremony IN ('registration','assertion')),
  session_data jsonb NOT NULL,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS staff_webauthn_challenges_expiry_idx
  ON testagram_auth.staff_webauthn_challenges(expires_at);

REVOKE ALL ON testagram_auth.staff_webauthn_users,
  testagram_auth.staff_webauthn_credentials,
  testagram_auth.staff_webauthn_challenges FROM PUBLIC;

DO $grants$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    GRANT SELECT,INSERT,UPDATE,DELETE ON
      testagram_auth.staff_webauthn_users,
      testagram_auth.staff_webauthn_credentials,
      testagram_auth.staff_webauthn_challenges TO testagram_app;
    GRANT USAGE,SELECT ON SEQUENCE testagram_auth.staff_webauthn_credentials_id_seq TO testagram_app;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
    GRANT SELECT,INSERT,UPDATE,DELETE ON
      testagram_auth.staff_webauthn_users,
      testagram_auth.staff_webauthn_credentials,
      testagram_auth.staff_webauthn_challenges TO service_role;
    GRANT USAGE,SELECT ON SEQUENCE testagram_auth.staff_webauthn_credentials_id_seq TO service_role;
  END IF;
END;
$grants$;

COMMIT;
