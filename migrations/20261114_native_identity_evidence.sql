-- Native identity evidence metadata and private-object lifecycle.
-- Object bytes remain in a private S3-compatible bucket; PostgreSQL stores only
-- an opaque object key, validated content metadata and a SHA-256 digest.
BEGIN;

CREATE TABLE IF NOT EXISTS testagram_identity.verification_evidence (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  session_id UUID NOT NULL REFERENCES testagram_identity.verification_sessions(id) ON DELETE RESTRICT,
  object_key TEXT NOT NULL UNIQUE CHECK (
    length(object_key) BETWEEN 16 AND 512
    AND object_key !~ '(^|/)\.\.?(/|$)'
    AND position(chr(10) in object_key)=0
    AND position(chr(13) in object_key)=0
  ),
  content_type TEXT NOT NULL CHECK (content_type IN (
    'image/jpeg','image/png','image/webp','application/pdf'
  )),
  size_bytes BIGINT NOT NULL CHECK (size_bytes BETWEEN 1 AND 15728640),
  sha256 BYTEA NOT NULL CHECK (octet_length(sha256)=32),
  status TEXT NOT NULL DEFAULT 'upload_pending'
    CHECK (status IN ('upload_pending','verified','quarantined','deleted')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  uploaded_at TIMESTAMPTZ,
  verified_at TIMESTAMPTZ,
  retention_until TIMESTAMPTZ NOT NULL DEFAULT (now()+interval '30 days'),
  CHECK ((status='verified') = (verified_at IS NOT NULL)),
  CHECK ((status='upload_pending') = (uploaded_at IS NULL))
);

CREATE INDEX IF NOT EXISTS verification_evidence_session_idx
  ON testagram_identity.verification_evidence(session_id,created_at DESC);
CREATE INDEX IF NOT EXISTS verification_evidence_retention_idx
  ON testagram_identity.verification_evidence(retention_until)
  WHERE status IN ('upload_pending','verified','quarantined');

CREATE UNIQUE INDEX IF NOT EXISTS verification_evidence_one_live_per_session_idx
  ON testagram_identity.verification_evidence(session_id)
  WHERE status IN ('upload_pending','verified','quarantined');

REVOKE ALL ON TABLE testagram_identity.verification_evidence FROM PUBLIC;
DO $grants$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    GRANT SELECT,INSERT,UPDATE ON testagram_identity.verification_evidence TO testagram_app;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
    GRANT USAGE ON SCHEMA testagram_identity TO service_role;
    GRANT SELECT,INSERT,UPDATE ON testagram_identity.verification_evidence TO service_role;
  END IF;
END;
$grants$;

COMMENT ON TABLE testagram_identity.verification_evidence IS
  'Private evidence object metadata only. Raw identity evidence bytes are never stored in PostgreSQL; access is mediated by short-lived private-storage URLs.';

COMMIT;
