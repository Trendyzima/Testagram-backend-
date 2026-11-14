-- Do not expose virtual-storage media until all three independent providers
-- have been read back and verified against the authoritative size and SHA-256.
BEGIN;

ALTER TABLE testagram_media.assets
  DROP CONSTRAINT IF EXISTS assets_status_check;
ALTER TABLE testagram_media.assets
  ADD CONSTRAINT assets_status_check
  CHECK (status IN ('ready','replication_pending','deletion_pending','quarantine_pending','deleted','quarantined'));

CREATE OR REPLACE FUNCTION testagram_media.enqueue_pending_object_replication()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, testagram_media
AS $$
BEGIN
  IF NEW.status <> 'replication_pending'
     OR NEW.storage_namespace NOT IN ('media-private','media-public')
     OR NEW.storage_provider IS NULL
     OR NEW.checksum_sha256 !~ '^[0-9a-f]{64}$'
     OR NEW.size_bytes <= 0 THEN
    RETURN NEW;
  END IF;

  INSERT INTO testagram_media.media_replication_outbox
    (object_key,operation,owner_id,storage_namespace,content_type,size_bytes,checksum_sha256)
  VALUES
    (NEW.object_key,'replicate',NEW.owner_id,NEW.storage_namespace,NEW.content_type,
     NEW.size_bytes,lower(NEW.checksum_sha256))
  ON CONFLICT (object_key) DO UPDATE SET
    operation='replicate',
    owner_id=EXCLUDED.owner_id,
    storage_namespace=EXCLUDED.storage_namespace,
    content_type=EXCLUDED.content_type,
    size_bytes=EXCLUDED.size_bytes,
    checksum_sha256=EXCLUDED.checksum_sha256,
    status='pending',
    attempts=0,
    available_at=now(),
    lease_until=NULL,
    lease_token=NULL,
    completed_at=NULL,
    failed_at=NULL,
    last_error_code='',
    updated_at=now()
  WHERE testagram_media.media_replication_outbox.status='dead_letter'
     OR testagram_media.media_replication_outbox.checksum_sha256 IS DISTINCT FROM EXCLUDED.checksum_sha256;

  RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS media_replication_enqueue_pending_asset ON testagram_media.assets;
CREATE TRIGGER media_replication_enqueue_pending_asset
AFTER INSERT OR UPDATE OF status, storage_namespace, storage_provider, content_type, size_bytes, checksum_sha256
ON testagram_media.assets
FOR EACH ROW
WHEN (NEW.status = 'replication_pending')
EXECUTE FUNCTION testagram_media.enqueue_pending_object_replication();

REVOKE ALL ON FUNCTION testagram_media.enqueue_pending_object_replication() FROM PUBLIC;

-- Existing virtual-storage assets that lack three verified placements are made
-- non-publishable. The trigger above durably queues their repair/reconciliation.
UPDATE testagram_media.assets a
SET status='replication_pending', updated_at=now()
WHERE a.status='ready'
  AND a.storage_namespace IN ('media-private','media-public')
  AND a.storage_provider IS NOT NULL
  AND a.checksum_sha256 ~ '^[0-9a-f]{64}$'
  AND (
    SELECT count(DISTINCT r.provider)
    FROM testagram_media.storage_replicas r
    WHERE r.object_key=a.object_key
      AND r.state='verified'
      AND r.verified_at IS NOT NULL
      AND r.object_size_bytes=a.size_bytes
      AND r.checksum_sha256=lower(a.checksum_sha256)
      AND r.provider IN ('instabox-vps','cloudflare-r2','backblaze-b2')
  ) < 3;

COMMENT ON COLUMN testagram_media.assets.status IS
  'replication_pending is non-publishable; ready means the required three provider copies have been independently read-back and SHA-256 verified for virtual-storage media.';

COMMIT;
