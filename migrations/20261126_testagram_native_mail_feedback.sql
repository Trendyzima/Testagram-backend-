BEGIN;

-- Additive native-mail contract. Existing Mailjet event values remain readable.
ALTER TABLE testagram_mail.delivery_events
  DROP CONSTRAINT IF EXISTS testagram_mail_delivery_events_event_type_check,
  DROP CONSTRAINT IF EXISTS delivery_events_event_type_check;

ALTER TABLE testagram_mail.delivery_events
  ADD CONSTRAINT testagram_mail_delivery_events_event_type_check
  CHECK (event_type IN (
    'mailjet.sent','mailjet.bounce','mailjet.blocked','mailjet.spam',
    'smtp.accepted','dsn.delayed','dsn.bounced','arf.complaint','smtp.rejected'
  ));

ALTER TABLE testagram_mail.delivery_events
  ADD COLUMN IF NOT EXISTS transport TEXT NOT NULL DEFAULT 'legacy',
  ADD COLUMN IF NOT EXISTS enhanced_status_code TEXT,
  ADD COLUMN IF NOT EXISTS diagnostic_code TEXT,
  ADD COLUMN IF NOT EXISTS remote_mta TEXT,
  ADD COLUMN IF NOT EXISTS report_sha256 BYTEA;

CREATE TABLE IF NOT EXISTS testagram_mail.return_path_tokens (
  token_hash BYTEA PRIMARY KEY,
  outbox_id BIGINT NOT NULL UNIQUE REFERENCES testagram_mail.outbox(id) ON DELETE CASCADE,
  expires_at TIMESTAMPTZ NOT NULL DEFAULT now() + interval '45 days',
  consumed_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS testagram_mail_return_path_expiry
  ON testagram_mail.return_path_tokens(expires_at) WHERE consumed_at IS NULL;

CREATE TABLE IF NOT EXISTS testagram_mail.inbound_reports (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  token_hash BYTEA NOT NULL,
  report_sha256 BYTEA NOT NULL,
  event_type TEXT NOT NULL CHECK (event_type IN ('dsn.delayed','dsn.bounced','arf.complaint')),
  enhanced_status_code TEXT,
  diagnostic_code TEXT,
  remote_mta TEXT,
  received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(token_hash, report_sha256)
);
CREATE INDEX IF NOT EXISTS testagram_mail_inbound_reports_received
  ON testagram_mail.inbound_reports(received_at,id);

REVOKE ALL ON testagram_mail.return_path_tokens FROM PUBLIC;
REVOKE ALL ON testagram_mail.inbound_reports FROM PUBLIC;
DO $grants$
BEGIN
  GRANT USAGE ON SCHEMA testagram_mail TO testagram_app;
  GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_mail.return_path_tokens TO testagram_app;
  GRANT SELECT,INSERT ON testagram_mail.inbound_reports TO testagram_app;
  GRANT USAGE,SELECT ON SEQUENCE testagram_mail_inbound_reports_id_seq TO testagram_app;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
    GRANT USAGE ON SCHEMA testagram_mail TO service_role;
    GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_mail.return_path_tokens TO service_role;
    GRANT SELECT,INSERT ON testagram_mail.inbound_reports TO service_role;
    GRANT USAGE,SELECT ON SEQUENCE testagram_mail_inbound_reports_id_seq TO service_role;
  END IF;
END;
$grants$;
COMMIT;
