-- Explicit, granular opt-in for social activity emails.
-- All email notifications are disabled until the account owner affirmatively enables them.
BEGIN;

CREATE TABLE IF NOT EXISTS testagram_local.email_notification_preferences (
  user_id UUID PRIMARY KEY REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  new_follower_email_enabled BOOLEAN NOT NULL DEFAULT FALSE,
  new_post_email_enabled BOOLEAN NOT NULL DEFAULT FALSE,
  consent_version TEXT NOT NULL DEFAULT '2026-11-24-v1'
    CHECK (length(consent_version) BETWEEN 1 AND 32),
  new_follower_consent_at TIMESTAMPTZ,
  new_follower_consent_withdrawn_at TIMESTAMPTZ,
  new_post_consent_at TIMESTAMPTZ,
  new_post_consent_withdrawn_at TIMESTAMPTZ,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (NOT new_follower_email_enabled OR (new_follower_consent_at IS NOT NULL AND new_follower_consent_withdrawn_at IS NULL)),
  CHECK (new_follower_consent_withdrawn_at IS NULL OR new_follower_consent_at IS NOT NULL),
  CHECK (NOT new_post_email_enabled OR (new_post_consent_at IS NOT NULL AND new_post_consent_withdrawn_at IS NULL)),
  CHECK (new_post_consent_withdrawn_at IS NULL OR new_post_consent_at IS NOT NULL)
);

ALTER TABLE testagram_mail.outbox
  ADD COLUMN IF NOT EXISTS preference_user_id UUID,
  ADD COLUMN IF NOT EXISTS preference_category TEXT;

DO $outbox_constraints$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid='testagram_mail.outbox'::regclass
      AND conname='testagram_mail_outbox_preference_category_check'
  ) THEN
    ALTER TABLE testagram_mail.outbox
      ADD CONSTRAINT testagram_mail_outbox_preference_category_check
      CHECK (preference_category IS NULL OR preference_category IN ('new_follower','new_post'));
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid='testagram_mail.outbox'::regclass
      AND conname='testagram_mail_outbox_preference_pair_check'
  ) THEN
    ALTER TABLE testagram_mail.outbox
      ADD CONSTRAINT testagram_mail_outbox_preference_pair_check
      CHECK ((preference_user_id IS NULL) = (preference_category IS NULL));
  END IF;
END;
$outbox_constraints$;

CREATE INDEX IF NOT EXISTS testagram_mail_outbox_social_consent_pending
  ON testagram_mail.outbox(preference_user_id,preference_category,id)
  WHERE status='pending' AND preference_user_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS testagram_local_email_preferences_followers
  ON testagram_local.email_notification_preferences(user_id)
  WHERE new_follower_email_enabled OR new_post_email_enabled;

REVOKE ALL ON testagram_local.email_notification_preferences FROM PUBLIC;

DO $grants$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    GRANT USAGE ON SCHEMA testagram_local TO testagram_app;
    GRANT SELECT,INSERT,UPDATE ON testagram_local.email_notification_preferences TO testagram_app;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
    GRANT USAGE ON SCHEMA testagram_local TO service_role;
    GRANT SELECT,INSERT,UPDATE ON testagram_local.email_notification_preferences TO service_role;
  END IF;
END;
$grants$;

COMMIT;
