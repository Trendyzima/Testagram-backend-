-- Keep the short-lived client-upload capability separate from the sealed
-- evidence object consumed by reviewers. Presigned PUT URLs can be reused until
-- expiry, so the reviewed object must never be the writable staging key.
BEGIN;

ALTER TABLE testagram_identity.verification_evidence
  ADD COLUMN IF NOT EXISTS staging_object_key TEXT;

UPDATE testagram_identity.verification_evidence
SET staging_object_key=object_key
WHERE staging_object_key IS NULL AND status='upload_pending';

CREATE UNIQUE INDEX IF NOT EXISTS verification_evidence_staging_object_key_uidx
  ON testagram_identity.verification_evidence(staging_object_key)
  WHERE staging_object_key IS NOT NULL;


-- Evidence metadata is append-only apart from the two explicit lifecycle
-- transitions: upload_pending -> verified (sealed key promotion) and
-- upload_pending/verified/quarantined -> deleted (retention cleanup).
CREATE OR REPLACE FUNCTION testagram_identity.enforce_verification_evidence_lifecycle()
RETURNS trigger
LANGUAGE plpgsql
AS $function$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'upload_pending'
       OR NEW.uploaded_at IS NOT NULL
       OR NEW.verified_at IS NOT NULL
       OR NEW.staging_object_key IS NULL
       OR NEW.object_key IS DISTINCT FROM NEW.staging_object_key THEN
      RAISE EXCEPTION 'identity evidence must begin as an unverified staging upload';
    END IF;
    RETURN NEW;
  END IF;

  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'identity evidence metadata is retained and cannot be deleted directly';
  END IF;

  IF OLD.status = 'upload_pending' AND NEW.status = 'verified' THEN
    IF NEW.id IS DISTINCT FROM OLD.id
       OR NEW.session_id IS DISTINCT FROM OLD.session_id
       OR NEW.staging_object_key IS DISTINCT FROM COALESCE(OLD.staging_object_key, OLD.object_key)
       OR NEW.object_key IS NOT DISTINCT FROM OLD.object_key
       OR NEW.content_type IS DISTINCT FROM OLD.content_type
       OR NEW.size_bytes IS DISTINCT FROM OLD.size_bytes
       OR NEW.sha256 IS DISTINCT FROM OLD.sha256
       OR NEW.created_at IS DISTINCT FROM OLD.created_at
       OR NEW.retention_until IS DISTINCT FROM OLD.retention_until
       OR NEW.uploaded_at IS NULL
       OR NEW.verified_at IS NULL THEN
      RAISE EXCEPTION 'verified identity evidence must be an immutable sealed promotion';
    END IF;
    RETURN NEW;
  END IF;

  IF OLD.status IN ('upload_pending','verified','quarantined') AND NEW.status = 'deleted' THEN
    IF NEW.id IS DISTINCT FROM OLD.id
       OR NEW.session_id IS DISTINCT FROM OLD.session_id
       OR NEW.object_key IS DISTINCT FROM OLD.object_key
       OR NEW.staging_object_key IS DISTINCT FROM OLD.staging_object_key
       OR NEW.content_type IS DISTINCT FROM OLD.content_type
       OR NEW.size_bytes IS DISTINCT FROM OLD.size_bytes
       OR NEW.sha256 IS DISTINCT FROM OLD.sha256
       OR NEW.created_at IS DISTINCT FROM OLD.created_at
       OR NEW.retention_until IS DISTINCT FROM OLD.retention_until
       OR NEW.uploaded_at IS NULL
       OR NEW.verified_at IS NOT NULL THEN
      RAISE EXCEPTION 'identity evidence retention cleanup may only mark immutable metadata deleted';
    END IF;
    RETURN NEW;
  END IF;

  RAISE EXCEPTION 'unsupported identity evidence lifecycle transition: % -> %', OLD.status, NEW.status;
END;
$function$;

DROP TRIGGER IF EXISTS verification_evidence_lifecycle_guard
  ON testagram_identity.verification_evidence;

CREATE TRIGGER verification_evidence_lifecycle_guard
BEFORE INSERT OR UPDATE OR DELETE ON testagram_identity.verification_evidence
FOR EACH ROW EXECUTE FUNCTION testagram_identity.enforce_verification_evidence_lifecycle();

COMMENT ON COLUMN testagram_identity.verification_evidence.object_key IS
  'Sealed private evidence object key used by reviewers after checksum-verified promotion.';
COMMENT ON COLUMN testagram_identity.verification_evidence.staging_object_key IS
  'Short-lived applicant upload target. Never expose this key to reviewers; the writable presigned URL cannot mutate the sealed object.';

COMMIT;
