BEGIN;

CREATE SCHEMA IF NOT EXISTS testagram_mail;

CREATE TABLE IF NOT EXISTS testagram_mail.outbox (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  payload_ciphertext BYTEA NOT NULL,
  payload_nonce BYTEA NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending','sending','sent','dead')),
  attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  lease_until TIMESTAMPTZ,
  sent_at TIMESTAMPTZ,
  provider_message_id TEXT,
  last_error_code TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK ((status='sending' AND lease_until IS NOT NULL) OR status<>'sending')
);

CREATE INDEX IF NOT EXISTS testagram_mail_outbox_ready_idx
  ON testagram_mail.outbox(status,available_at,id)
  WHERE status IN ('pending','sending');

REVOKE ALL ON SCHEMA testagram_mail FROM PUBLIC;
REVOKE ALL ON TABLE testagram_mail.outbox FROM PUBLIC;

DO $grants$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    GRANT USAGE ON SCHEMA testagram_mail TO testagram_app;
    GRANT SELECT,INSERT,UPDATE ON testagram_mail.outbox TO testagram_app;
    GRANT USAGE,SELECT ON SEQUENCE testagram_mail.outbox_id_seq TO testagram_app;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
    GRANT USAGE ON SCHEMA testagram_mail TO service_role;
    GRANT SELECT,INSERT,UPDATE ON testagram_mail.outbox TO service_role;
    GRANT USAGE,SELECT ON SEQUENCE testagram_mail.outbox_id_seq TO service_role;
  END IF;
END;
$grants$;

COMMIT;
