-- Durable, auditable identity-registration state.
-- This migration deliberately adds storage primitives only. It does not enable
-- public registration or mint approval tokens; application handlers must use
-- these records transactionally before the feature flag can be enabled.
BEGIN;

CREATE TABLE IF NOT EXISTS testagram_identity.identity_applications (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  email TEXT NOT NULL CHECK (length(email) BETWEEN 3 AND 254 AND email = lower(btrim(email))),
  username TEXT NOT NULL CHECK (username ~ '^[A-Za-z0-9_]{3,30}$'),
  display_name TEXT NOT NULL CHECK (length(btrim(display_name)) BETWEEN 2 AND 80),
  adult_confirmed_at TIMESTAMPTZ NOT NULL,
  adult_verified_at TIMESTAMPTZ,
  evidence_reference TEXT NOT NULL CHECK (length(btrim(evidence_reference)) BETWEEN 8 AND 200),
  evidence_fingerprint_sha256 BYTEA NOT NULL CHECK (octet_length(evidence_fingerprint_sha256) = 32),
  terms_version TEXT NOT NULL CHECK (length(btrim(terms_version)) BETWEEN 1 AND 64),
  privacy_version TEXT NOT NULL CHECK (length(btrim(privacy_version)) BETWEEN 1 AND 64),
  terms_accepted_at TIMESTAMPTZ NOT NULL,
  privacy_accepted_at TIMESTAMPTZ NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending','under_review','approved','rejected','provisioning','provisioned','verification_pending','verified','expired','cancelled')),
  submitted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (adult_verified_at IS NULL OR adult_verified_at >= submitted_at - interval '10 years')
);

CREATE INDEX IF NOT EXISTS identity_applications_review_queue_idx
  ON testagram_identity.identity_applications(status, submitted_at)
  WHERE status IN ('pending','under_review');
CREATE INDEX IF NOT EXISTS identity_applications_email_idx
  ON testagram_identity.identity_applications(email, submitted_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS identity_applications_active_fingerprint_idx
  ON testagram_identity.identity_applications(evidence_fingerprint_sha256)
  WHERE status NOT IN ('rejected','expired','cancelled');

CREATE TABLE IF NOT EXISTS testagram_identity.identity_application_reviews (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  application_id UUID NOT NULL REFERENCES testagram_identity.identity_applications(id) ON DELETE RESTRICT,
  reviewer_appwrite_user_id TEXT NOT NULL,
  decision TEXT NOT NULL CHECK (decision IN ('approved','rejected')),
  reason TEXT NOT NULL CHECK (length(btrim(reason)) BETWEEN 8 AND 1000),
  evidence_audit_reference TEXT NOT NULL CHECK (length(btrim(evidence_audit_reference)) BETWEEN 8 AND 200),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(application_id, reviewer_appwrite_user_id)
);

CREATE INDEX IF NOT EXISTS identity_application_reviews_application_idx
  ON testagram_identity.identity_application_reviews(application_id, created_at);

CREATE TABLE IF NOT EXISTS testagram_identity.identity_approval_tokens (
  token_sha256 BYTEA PRIMARY KEY CHECK (octet_length(token_sha256) = 32),
  jti_sha256 BYTEA NOT NULL UNIQUE CHECK (octet_length(jti_sha256) = 32),
  application_id UUID NOT NULL UNIQUE REFERENCES testagram_identity.identity_applications(id) ON DELETE RESTRICT,
  first_approver_appwrite_user_id TEXT NOT NULL,
  second_approver_appwrite_user_id TEXT NOT NULL,
  issued_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ NOT NULL,
  consumed_at TIMESTAMPTZ,
  consumed_by_appwrite_user_id TEXT,
  CHECK (first_approver_appwrite_user_id <> second_approver_appwrite_user_id),
  CHECK (expires_at > issued_at AND expires_at <= issued_at + interval '15 minutes'),
  CHECK ((consumed_at IS NULL) = (consumed_by_appwrite_user_id IS NULL))
);

CREATE INDEX IF NOT EXISTS identity_approval_tokens_expiry_idx
  ON testagram_identity.identity_approval_tokens(expires_at)
  WHERE consumed_at IS NULL;

CREATE TABLE IF NOT EXISTS testagram_identity.registration_provisioning (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  application_id UUID NOT NULL UNIQUE REFERENCES testagram_identity.identity_applications(id) ON DELETE RESTRICT,
  idempotency_key_sha256 BYTEA NOT NULL UNIQUE CHECK (octet_length(idempotency_key_sha256) = 32),
  appwrite_user_id TEXT UNIQUE,
  canonical_user_id UUID UNIQUE,
  status TEXT NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending','appwrite_created','linked','verification_sent','completed','compensation_required','failed')),
  attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 20),
  last_error_code TEXT NOT NULL DEFAULT '' CHECK (length(last_error_code) <= 64),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK ((status IN ('linked','verification_sent','completed')) = (appwrite_user_id IS NOT NULL AND canonical_user_id IS NOT NULL))
);

CREATE TABLE IF NOT EXISTS testagram_identity.legal_consent_audit (
  id BIGSERIAL PRIMARY KEY,
  application_id UUID NOT NULL REFERENCES testagram_identity.identity_applications(id) ON DELETE RESTRICT,
  consent_type TEXT NOT NULL CHECK (consent_type IN ('terms','privacy','age_confirmation')),
  policy_version TEXT NOT NULL CHECK (length(btrim(policy_version)) BETWEEN 1 AND 64),
  accepted_at TIMESTAMPTZ NOT NULL,
  request_id TEXT NOT NULL DEFAULT '' CHECK (length(request_id) <= 128),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(application_id, consent_type, policy_version)
);

CREATE INDEX IF NOT EXISTS legal_consent_audit_application_idx
  ON testagram_identity.legal_consent_audit(application_id, created_at);

REVOKE ALL ON TABLE
  testagram_identity.identity_applications,
  testagram_identity.identity_application_reviews,
  testagram_identity.identity_approval_tokens,
  testagram_identity.registration_provisioning,
  testagram_identity.legal_consent_audit
FROM PUBLIC;

DO $grants$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'testagram_app') THEN
    GRANT SELECT, INSERT, UPDATE ON testagram_identity.identity_applications TO testagram_app;
    GRANT SELECT, INSERT ON testagram_identity.identity_application_reviews TO testagram_app;
    GRANT SELECT, INSERT, UPDATE ON testagram_identity.identity_approval_tokens TO testagram_app;
    GRANT SELECT, INSERT, UPDATE ON testagram_identity.registration_provisioning TO testagram_app;
    GRANT SELECT, INSERT ON testagram_identity.legal_consent_audit TO testagram_app;
    GRANT USAGE, SELECT ON SEQUENCE testagram_identity.legal_consent_audit_id_seq TO testagram_app;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'service_role') THEN
    GRANT USAGE ON SCHEMA testagram_identity TO service_role;
    GRANT SELECT, INSERT, UPDATE ON testagram_identity.identity_applications TO service_role;
    GRANT SELECT, INSERT ON testagram_identity.identity_application_reviews TO service_role;
    GRANT SELECT, INSERT, UPDATE ON testagram_identity.identity_approval_tokens TO service_role;
    GRANT SELECT, INSERT, UPDATE ON testagram_identity.registration_provisioning TO service_role;
    GRANT SELECT, INSERT ON testagram_identity.legal_consent_audit TO service_role;
    GRANT USAGE, SELECT ON SEQUENCE testagram_identity.legal_consent_audit_id_seq TO service_role;
  END IF;
END;
$grants$;

COMMENT ON TABLE testagram_identity.identity_applications IS
  'Registration applications. Store only opaque evidence references and a non-reversible fingerprint digest; never store identity-document bytes here.';
COMMENT ON TABLE testagram_identity.identity_approval_tokens IS
  'Single-use approval-token redemption ledger. Application handlers must consume token_sha256 and JTI atomically with provisioning state transitions.';
COMMENT ON TABLE testagram_identity.registration_provisioning IS
  'Idempotent Appwrite-to-canonical-account provisioning saga state; external Appwrite calls require compensating cleanup on database failure.';
COMMENT ON TABLE testagram_identity.legal_consent_audit IS
  'Append-only versioned Terms, Privacy and age-confirmation acceptance evidence.';

COMMIT;
