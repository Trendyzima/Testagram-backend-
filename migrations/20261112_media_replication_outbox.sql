-- Durable, transactional object-copy queue. A ready asset and its replication
-- intent commit together; a process crash cannot silently lose the copy request.
BEGIN;

CREATE TABLE IF NOT EXISTS testagram_media.media_replication_outbox (
  id BIGSERIAL PRIMARY KEY,
  object_key TEXT NOT NULL REFERENCES testagram_media.assets(object_key) ON DELETE CASCADE,
  owner_id TEXT NOT NULL,
  storage_namespace TEXT NOT NULL CHECK (storage_namespace IN ('media-private','media-public')),
  content_type TEXT NOT NULL,
  size_bytes BIGINT NOT NULL CHECK (size_bytes > 0),
  checksum_sha256 TEXT NOT NULL CHECK (checksum_sha256 ~ '^[0-9a-f]{64}$'),
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','processing','complete','dead_letter')),
  attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  lease_until TIMESTAMPTZ,
  lease_token UUID,
  completed_at TIMESTAMPTZ,
  failed_at TIMESTAMPTZ,
  last_error_code TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (object_key),
  CHECK ((status = 'processing' AND lease_until IS NOT NULL AND lease_token IS NOT NULL)
      OR status <> 'processing'),
  CHECK ((status = 'complete' AND completed_at IS NOT NULL) OR status <> 'complete'),
  CHECK ((status = 'dead_letter' AND failed_at IS NOT NULL) OR status <> 'dead_letter')
);

CREATE INDEX IF NOT EXISTS media_replication_outbox_ready_idx
  ON testagram_media.media_replication_outbox(available_at, created_at)
  WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS media_replication_outbox_lease_idx
  ON testagram_media.media_replication_outbox(lease_until)
  WHERE status = 'processing';

ALTER TABLE testagram_media.media_replication_outbox ENABLE ROW LEVEL SECURITY;
REVOKE ALL ON testagram_media.media_replication_outbox FROM PUBLIC;

CREATE OR REPLACE FUNCTION testagram_media.enqueue_object_replication()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, testagram_media
AS $$
BEGIN
  IF NEW.status = 'ready'
     AND NEW.storage_namespace IN ('media-private','media-public')
     AND NEW.storage_provider IS NOT NULL
     AND NEW.checksum_sha256 ~ '^[0-9a-f]{64}$'
     AND NEW.size_bytes > 0 THEN
    INSERT INTO testagram_media.media_replication_outbox
      (object_key, owner_id, storage_namespace, content_type, size_bytes, checksum_sha256)
    VALUES
      (NEW.object_key, NEW.owner_id, NEW.storage_namespace, NEW.content_type,
       NEW.size_bytes, lower(NEW.checksum_sha256))
    ON CONFLICT (object_key) DO UPDATE SET
      owner_id = EXCLUDED.owner_id,
      storage_namespace = EXCLUDED.storage_namespace,
      content_type = EXCLUDED.content_type,
      size_bytes = EXCLUDED.size_bytes,
      checksum_sha256 = EXCLUDED.checksum_sha256,
      status = 'pending',
      attempts = 0,
      available_at = now(),
      lease_until = NULL,
      lease_token = NULL,
      completed_at = NULL,
      failed_at = NULL,
      last_error_code = '',
      updated_at = now()
    WHERE testagram_media.media_replication_outbox.checksum_sha256 <> EXCLUDED.checksum_sha256
       OR testagram_media.media_replication_outbox.status = 'dead_letter';
  END IF;
  RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS media_replication_enqueue_after_asset_write ON testagram_media.assets;
CREATE TRIGGER media_replication_enqueue_after_asset_write
AFTER INSERT OR UPDATE OF status, storage_namespace, storage_provider, content_type, size_bytes, checksum_sha256
ON testagram_media.assets
FOR EACH ROW EXECUTE FUNCTION testagram_media.enqueue_object_replication();

REVOKE ALL ON FUNCTION testagram_media.enqueue_object_replication() FROM PUBLIC;

CREATE OR REPLACE VIEW testagram_media.media_replication_health AS
SELECT
  count(*) FILTER (WHERE status='pending') AS pending,
  count(*) FILTER (WHERE status='processing') AS processing,
  count(*) FILTER (WHERE status='dead_letter') AS dead_letter,
  COALESCE(EXTRACT(EPOCH FROM (now()-min(created_at) FILTER (WHERE status IN ('pending','processing')))),0)::BIGINT AS oldest_pending_seconds,
  (SELECT count(*) FROM testagram_media.storage_replicas WHERE state='verified') AS verified_placements,
  (SELECT count(DISTINCT object_key) FROM testagram_media.storage_replicas WHERE state='verified') AS objects_with_verified_placements
FROM testagram_media.media_replication_outbox;

REVOKE ALL ON testagram_media.media_replication_health FROM PUBLIC;

COMMENT ON TABLE testagram_media.media_replication_outbox IS
  'Transactional durable queue for verified object copies to independent media providers. Private metadata stays server-side; no signed URLs or credentials are stored.';

COMMIT;
