-- Native manual-review intake stores the applicant's declaration before
-- evidence is uploaded. The evidence fingerprint becomes mandatory only when
-- evidence is submitted; the submission transaction writes the verified digest.
-- This additive migration does not enable public registration.
BEGIN;

ALTER TABLE testagram_identity.identity_applications
  ALTER COLUMN evidence_fingerprint_sha256 DROP NOT NULL;

COMMENT ON COLUMN testagram_identity.identity_applications.evidence_fingerprint_sha256 IS
  'NULL during initial native application intake; populated with the verified evidence SHA-256 at evidence submission.';


-- Initial intake may have no evidence digest, but a provisioning approval
-- token must never be issued until a verified age-assurance session records
-- the exact same evidence reference and SHA-256 digest as the application.
CREATE OR REPLACE FUNCTION testagram_identity.assert_native_identity_evidence_complete()
RETURNS trigger
LANGUAGE plpgsql
AS $function$
DECLARE
  v_evidence_reference TEXT;
  v_evidence_fingerprint BYTEA;
  v_verified_session_count INTEGER;
BEGIN
  SELECT evidence_reference,evidence_fingerprint_sha256
    INTO v_evidence_reference,v_evidence_fingerprint
  FROM testagram_identity.identity_applications
  WHERE id=NEW.application_id
  FOR UPDATE;

  IF NOT FOUND OR v_evidence_fingerprint IS NULL
     OR length(btrim(COALESCE(v_evidence_reference,''))) < 8 THEN
    RAISE EXCEPTION 'verified identity evidence is required before provisioning approval';
  END IF;

  SELECT count(*) INTO v_verified_session_count
  FROM testagram_identity.verification_sessions
  WHERE application_id=NEW.application_id
    AND purpose='age_assurance'
    AND status='verified'
    AND verified_age_over_threshold IS TRUE
    AND assurance_level IN ('reviewed','substantial','high')
    AND evidence_reference=v_evidence_reference
    AND evidence_fingerprint_sha256=v_evidence_fingerprint;

  IF v_verified_session_count < 1 THEN
    RAISE EXCEPTION 'approval evidence must match a completed verified age-assurance session';
  END IF;

  RETURN NEW;
END;
$function$;

DROP TRIGGER IF EXISTS identity_approval_token_requires_verified_evidence
  ON testagram_identity.identity_approval_tokens;

CREATE TRIGGER identity_approval_token_requires_verified_evidence
BEFORE INSERT ON testagram_identity.identity_approval_tokens
FOR EACH ROW EXECUTE FUNCTION testagram_identity.assert_native_identity_evidence_complete();

COMMIT;
