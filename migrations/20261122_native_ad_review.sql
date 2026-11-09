-- Native Testagram advertising review domain.
-- Privileged review actions are authenticated by Appwrite and require a
-- current session that has recently completed a second factor.
BEGIN;

CREATE SCHEMA IF NOT EXISTS testagram_identity;

CREATE TABLE IF NOT EXISTS testagram_local.advertisers (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  owner_user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused', 'disabled')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS testagram_local.ad_campaigns (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  advertiser_id UUID NOT NULL REFERENCES testagram_local.advertisers(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  objective TEXT NOT NULL DEFAULT 'traffic',
  status TEXT NOT NULL DEFAULT 'draft'
    CHECK (status IN ('draft', 'pending_payment', 'pending_review', 'active', 'paused', 'completed', 'rejected', 'archived')),
  payment_status TEXT NOT NULL DEFAULT 'unpaid'
    CHECK (payment_status IN ('unpaid', 'pending', 'funded', 'failed', 'refunded')),
  currency TEXT NOT NULL DEFAULT 'KES' CHECK (currency ~ '^[A-Z]{3}$'),
  lifetime_budget_micros BIGINT NOT NULL DEFAULT 0 CHECK (lifetime_budget_micros >= 0),
  funded_micros BIGINT NOT NULL DEFAULT 0 CHECK (funded_micros >= 0),
  daily_budget_micros BIGINT CHECK (daily_budget_micros IS NULL OR daily_budget_micros >= 0),
  bid_cpm_micros BIGINT NOT NULL DEFAULT 50000 CHECK (bid_cpm_micros >= 0),
  priority INTEGER NOT NULL DEFAULT 50 CHECK (priority BETWEEN 0 AND 100),
  starts_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  ends_at TIMESTAMPTZ,
  targeting JSONB NOT NULL DEFAULT '{}'::jsonb,
  payment_reference TEXT,
  moderation_status TEXT NOT NULL DEFAULT 'pending'
    CHECK (moderation_status IN ('pending', 'approved', 'rejected')),
  moderation_note TEXT,
  moderated_by UUID REFERENCES testagram_local.accounts(user_id) ON DELETE SET NULL,
  moderated_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (ends_at IS NULL OR ends_at > starts_at)
);

CREATE TABLE IF NOT EXISTS testagram_local.ad_creatives (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  campaign_id UUID NOT NULL REFERENCES testagram_local.ad_campaigns(id) ON DELETE CASCADE,
  format TEXT NOT NULL DEFAULT 'display' CHECK (format IN ('display', 'story', 'video')),
  headline TEXT NOT NULL,
  body TEXT,
  cta TEXT NOT NULL DEFAULT 'Learn more',
  asset_url TEXT,
  click_through_url TEXT NOT NULL,
  weight INTEGER NOT NULL DEFAULT 1 CHECK (weight > 0),
  enabled BOOLEAN NOT NULL DEFAULT true,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS testagram_identity.staff_permissions (
  user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  permission_key TEXT NOT NULL CHECK (permission_key IN ('platform.ads')),
  granted_by UUID REFERENCES testagram_local.accounts(user_id) ON DELETE SET NULL,
  granted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  revoked_at TIMESTAMPTZ,
  PRIMARY KEY (user_id, permission_key)
);

CREATE TABLE IF NOT EXISTS testagram_local.ad_review_audit (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  campaign_id UUID NOT NULL REFERENCES testagram_local.ad_campaigns(id) ON DELETE CASCADE,
  advertiser_owner_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  actor_user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE RESTRICT,
  decision TEXT NOT NULL CHECK (decision IN ('approved', 'rejected')),
  note TEXT,
  previous_status TEXT NOT NULL,
  resulting_status TEXT NOT NULL,
  payment_status TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS testagram_local.ad_campaign_status_audit (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  campaign_id UUID NOT NULL REFERENCES testagram_local.ad_campaigns(id) ON DELETE CASCADE,
  actor_user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE RESTRICT,
  previous_status TEXT NOT NULL,
  resulting_status TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS testagram_local_ad_review_queue_idx
  ON testagram_local.ad_campaigns(moderation_status, created_at DESC);
CREATE INDEX IF NOT EXISTS testagram_local_ad_creatives_campaign_idx
  ON testagram_local.ad_creatives(campaign_id, created_at);
CREATE INDEX IF NOT EXISTS testagram_identity_staff_permissions_active_idx
  ON testagram_identity.staff_permissions(user_id, permission_key)
  WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS testagram_local_ad_review_audit_campaign_idx
  ON testagram_local.ad_review_audit(campaign_id, created_at DESC);
CREATE INDEX IF NOT EXISTS testagram_local_ad_status_audit_campaign_idx
  ON testagram_local.ad_campaign_status_audit(campaign_id, created_at DESC);

-- Preserve already funded, serving campaigns if this migration is applied to
-- a database that already has the campaign tables.
UPDATE testagram_local.ad_campaigns
SET moderation_status = 'approved',
    moderated_at = COALESCE(moderated_at, updated_at)
WHERE status = 'active'
  AND payment_status = 'funded'
  AND moderation_status = 'pending';

REVOKE ALL ON testagram_local.advertisers,
  testagram_local.ad_campaigns,
  testagram_local.ad_creatives,
  testagram_local.ad_review_audit,
  testagram_local.ad_campaign_status_audit,
  testagram_identity.staff_permissions
FROM PUBLIC;

DO $grants$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'testagram_app') THEN
    GRANT SELECT, INSERT, UPDATE ON testagram_local.advertisers,
      testagram_local.ad_campaigns,
      testagram_local.ad_creatives,
      testagram_local.ad_review_audit,
      testagram_local.ad_campaign_status_audit
    TO testagram_app;
    GRANT SELECT, INSERT, UPDATE ON testagram_identity.staff_permissions TO testagram_app;
  END IF;

  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'service_role') THEN
    GRANT SELECT, INSERT, UPDATE ON testagram_local.advertisers,
      testagram_local.ad_campaigns,
      testagram_local.ad_creatives,
      testagram_local.ad_review_audit,
      testagram_local.ad_campaign_status_audit
    TO service_role;
    GRANT SELECT, INSERT, UPDATE ON testagram_identity.staff_permissions TO service_role;
  END IF;
END;
$grants$;

COMMIT;
