-- Native manual-review intake stores the applicant's declaration before
-- evidence is uploaded. The evidence fingerprint becomes mandatory only when
-- evidence is submitted; the submission transaction writes the verified digest.
-- This additive migration does not enable public registration.
BEGIN;

ALTER TABLE testagram_identity.identity_applications
  ALTER COLUMN evidence_fingerprint_sha256 DROP NOT NULL;

COMMENT ON COLUMN testagram_identity.identity_applications.evidence_fingerprint_sha256 IS
  'NULL during initial native application intake; populated with the verified evidence SHA-256 at evidence submission.';

COMMIT;
