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
  new_post_consent_at TIMESTAMPTZ,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (new_follower_email_enabled = (new_follower_consent_at IS NOT NULL)),
  CHECK (new_post_email_enabled = (new_post_consent_at IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS testagram_local_email_preferences_followers
  ON testagram_local.email_notification_preferences(user_id)
  WHERE new_follower_email_enabled OR new_post_email_enabled;

REVOKE ALL ON testagram_local.email_notification_preferences FROM PUBLIC;

DO $grants$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    GRANT SELECT,INSERT,UPDATE ON testagram_local.email_notification_preferences TO testagram_app;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
    GRANT SELECT,INSERT,UPDATE ON testagram_local.email_notification_preferences TO service_role;
  END IF;
END;
$grants$;

COMMIT;
