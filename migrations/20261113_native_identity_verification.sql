-- Testagram-owned identity and age-assurance workflow.
-- No external verification vendor, webhook, callback secret, or vendor session is required.
-- Raw identity documents/selfies MUST NOT be stored in PostgreSQL. Evidence is referenced
-- through opaque private-storage handles and access is controlled by the evidence service.
BEGIN;

CREATE TABLE IF NOT EXISTS testagram_identity.verification_sessions (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  application_id UUID NOT NULL REFERENCES testagram_identity.identity_applications(id) ON DELETE RESTRICT,
  session_token_sha256 BYTEA NOT NULL UNIQUE CHECK (octet_length(session_token_sha256)=32),
  purpose TEXT NOT NULL CHECK (purpose IN ('age_assurance','identity_assurance')),
  requested_minimum_age SMALLINT,
  status TEXT NOT NULL DEFAULT 'created'
    CHECK (status IN ('created','in_progress','submitted','manual_review','verified','failed','expired','cancelled')),
  evidence_reference TEXT,
  evidence_fingerprint_sha256 BYTEA CHECK (evidence_fingerprint_sha256 IS NULL OR octet_length(evidence_fingerprint_sha256)=32),
  assurance_method TEXT NOT NULL DEFAULT 'manual_review'
    CHECK (assurance_method IN ('self_declaration','document_review','trusted_external_attestation','manual_review')),
  assurance_level TEXT NOT NULL DEFAULT 'none'
    CHECK (assurance_level IN ('none','self_asserted','reviewed','substantial','high')),
  verified_age_over_threshold BOOLEAN,
  decision_reason_code TEXT NOT NULL DEFAULT '' CHECK (length(decision_reason_code)<=64),
  expires_at TIMESTAMPTZ NOT NULL,
  submitted_at TIMESTAMPTZ,
  decided_at TIMESTAMPTZ,
  decided_by_appwrite_user_id TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (expires_at > created_at),
  CHECK ((status='verified') = (verified_age_over_threshold IS TRUE AND decided_at IS NOT NULL)),
  CHECK ((decided_by_appwrite_user_id IS NULL) = (decided_at IS NULL)),
  CHECK (status <> 'verified' OR assurance_level IN ('reviewed','substantial','high')),
  CHECK (status <> 'verified' OR evidence_reference IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS verification_sessions_application_idx
  ON testagram_identity.verification_sessions(application_id,created_at DESC);
CREATE INDEX IF NOT EXISTS verification_sessions_expiry_idx
  ON testagram_identity.verification_sessions(expires_at)
  WHERE status IN ('created','in_progress','submitted','manual_review');

CREATE UNIQUE INDEX IF NOT EXISTS verification_sessions_one_open_per_purpose_idx
  ON testagram_identity.verification_sessions(application_id,purpose)
  WHERE status IN ('created','in_progress','submitted','manual_review');

CREATE TABLE IF NOT EXISTS testagram_identity.verification_events (
  id BIGSERIAL PRIMARY KEY,
  session_id UUID NOT NULL REFERENCES testagram_identity.verification_sessions(id) ON DELETE RESTRICT,
  event_type TEXT NOT NULL CHECK (event_type IN (
    'session_created','session_started','evidence_submitted','review_requested',
    'decision_verified','decision_failed','decision_escalated','session_expired','session_cancelled'
  )),
  actor_type TEXT NOT NULL CHECK (actor_type IN ('applicant','reviewer','system')),
  actor_reference TEXT NOT NULL DEFAULT '' CHECK (length(actor_reference)<=160),
  request_id TEXT NOT NULL DEFAULT '' CHECK (length(request_id)<=128),
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata)='object'),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS verification_events_session_idx
  ON testagram_identity.verification_events(session_id,created_at);

CREATE TABLE IF NOT EXISTS testagram_identity.verification_reviews (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  session_id UUID NOT NULL REFERENCES testagram_identity.verification_sessions(id) ON DELETE RESTRICT,
  reviewer_appwrite_user_id TEXT NOT NULL,
  decision TEXT NOT NULL CHECK (decision IN ('verified','failed','escalate')),
  reason_code TEXT NOT NULL CHECK (length(btrim(reason_code)) BETWEEN 2 AND 64),
  evidence_reference TEXT NOT NULL CHECK (length(btrim(evidence_reference)) BETWEEN 8 AND 200),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(session_id,reviewer_appwrite_user_id)
);

CREATE INDEX IF NOT EXISTS verification_reviews_session_idx
  ON testagram_identity.verification_reviews(session_id,created_at);

REVOKE ALL ON TABLE
  testagram_identity.verification_sessions,
  testagram_identity.verification_events,
  testagram_identity.verification_reviews
FROM PUBLIC;

DO $grants$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    GRANT SELECT,INSERT,UPDATE ON testagram_identity.verification_sessions TO testagram_app;
    GRANT SELECT,INSERT ON testagram_identity.verification_events TO testagram_app;
    GRANT SELECT,INSERT ON testagram_identity.verification_reviews TO testagram_app;
    GRANT USAGE,SELECT ON SEQUENCE testagram_identity.verification_events_id_seq TO testagram_app;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
    GRANT USAGE ON SCHEMA testagram_identity TO service_role;
    GRANT SELECT,INSERT,UPDATE ON testagram_identity.verification_sessions TO service_role;
    GRANT SELECT,INSERT ON testagram_identity.verification_events TO service_role;
    GRANT SELECT,INSERT ON testagram_identity.verification_reviews TO service_role;
    GRANT USAGE,SELECT ON SEQUENCE testagram_identity.verification_events_id_seq TO service_role;
  END IF;
END;
$grants$;

COMMENT ON TABLE testagram_identity.verification_sessions IS
  'Testagram-owned verification sessions. Session bearer tokens are stored only as SHA-256 digests; raw evidence bytes live only in restricted private storage.';
COMMENT ON TABLE testagram_identity.verification_events IS
  'Append-only audit stream for Testagram-owned identity verification state transitions.';
COMMENT ON TABLE testagram_identity.verification_reviews IS
  'Reviewer decisions. Two distinct reviewers are required by the approval workflow for account activation; a single review never provisions an account.';

COMMIT;
