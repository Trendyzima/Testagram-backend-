BEGIN;

ALTER TABLE testagram_mail.outbox
  ADD COLUMN IF NOT EXISTS delivery_status TEXT NOT NULL DEFAULT 'accepted',
  ADD COLUMN IF NOT EXISTS delivered_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS delivery_failed_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS complained_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS recipient_fingerprint BYTEA;

DO $constraints$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid='testagram_mail.outbox'::regclass
      AND conname='testagram_mail_outbox_delivery_status_check'
  ) THEN
    ALTER TABLE testagram_mail.outbox
      ADD CONSTRAINT testagram_mail_outbox_delivery_status_check
      CHECK (delivery_status IN ('accepted','deferred','delivered','bounced','failed','rejected','complained'));
  END IF;
END;
$constraints$;

CREATE INDEX IF NOT EXISTS testagram_mail_outbox_provider_message_id
  ON testagram_mail.outbox(provider_message_id)
  WHERE provider_message_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS testagram_mail.delivery_events (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  provider_event_id TEXT NOT NULL UNIQUE,
  provider_message_id TEXT NOT NULL,
  event_type TEXT NOT NULL CHECK (event_type IN (
    'cf.email.sending.message.delivered',
    'cf.email.sending.message.deferred',
    'cf.email.sending.message.bounced',
    'cf.email.sending.message.failed',
    'cf.email.sending.message.rejected',
    'cf.email.sending.message.complained'
  )),
  recipient_fingerprint BYTEA NOT NULL,
  smtp_status_code TEXT,
  bounce_type TEXT,
  rejection_reason TEXT,
  rejection_party TEXT,
  received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  processed_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS testagram_mail_delivery_events_unprocessed
  ON testagram_mail.delivery_events(received_at,id)
  WHERE processed_at IS NULL;

CREATE TABLE IF NOT EXISTS testagram_mail.recipient_suppressions (
  recipient_fingerprint BYTEA PRIMARY KEY,
  reason TEXT NOT NULL CHECK (reason IN ('hard_bounce','complaint','rejected')),
  source_event_id TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

REVOKE ALL ON testagram_mail.delivery_events FROM PUBLIC;
REVOKE ALL ON testagram_mail.recipient_suppressions FROM PUBLIC;

DO $grants$
BEGIN
  GRANT USAGE ON SCHEMA testagram_mail TO testagram_app;
  GRANT SELECT,INSERT,UPDATE ON testagram_mail.delivery_events TO testagram_app;
  GRANT SELECT,INSERT ON testagram_mail.recipient_suppressions TO testagram_app;
  GRANT USAGE,SELECT ON SEQUENCE testagram_mail.delivery_events_id_seq TO testagram_app;

  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
    GRANT USAGE ON SCHEMA testagram_mail TO service_role;
    GRANT SELECT,INSERT,UPDATE ON testagram_mail.delivery_events TO service_role;
    GRANT SELECT,INSERT ON testagram_mail.recipient_suppressions TO service_role;
    GRANT USAGE,SELECT ON SEQUENCE testagram_mail.delivery_events_id_seq TO service_role;
  END IF;
END;
$grants$;

COMMIT;
