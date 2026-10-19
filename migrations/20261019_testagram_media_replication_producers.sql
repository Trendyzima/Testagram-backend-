-- Transactional outbox producers for Testagram-owned media metadata.
-- This does not copy object bytes, signed URLs, or private media to peers.
-- Public metadata is emitted only when its checksum and object metadata are complete.
BEGIN;

CREATE TABLE IF NOT EXISTS platform_replication_source_versions (
  origin TEXT NOT NULL CHECK (origin = 'testagram-vps'),
  entity_type TEXT NOT NULL CHECK (entity_type ~ '^[a-z][a-z0-9_-]{0,31}$'),
  entity_id TEXT NOT NULL CHECK (length(entity_id) BETWEEN 1 AND 128),
  current_version BIGINT NOT NULL CHECK (current_version > 0),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (origin, entity_type, entity_id)
);

CREATE OR REPLACE FUNCTION public.next_testagram_replication_version(
  p_entity_type TEXT,
  p_entity_id TEXT
) RETURNS BIGINT
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
  next_version BIGINT;
BEGIN
  IF p_entity_type !~ '^[a-z][a-z0-9_-]{0,31}$'
     OR p_entity_id IS NULL OR length(p_entity_id) NOT BETWEEN 1 AND 128 THEN
    RAISE EXCEPTION 'invalid replication entity identity';
  END IF;

  PERFORM pg_advisory_xact_lock(hashtextextended('testagram-vps:' || p_entity_type || ':' || p_entity_id, 0));
  INSERT INTO public.platform_replication_source_versions(origin, entity_type, entity_id, current_version)
  VALUES ('testagram-vps', p_entity_type, p_entity_id, 1)
  ON CONFLICT (origin, entity_type, entity_id) DO UPDATE
    SET current_version = public.platform_replication_source_versions.current_version + 1,
        updated_at = now()
  RETURNING current_version INTO next_version;
  RETURN next_version;
END;
$$;

CREATE OR REPLACE FUNCTION public.enqueue_testagram_public_media_event(
  p_object_key TEXT,
  p_event_type TEXT,
  p_payload JSONB
) RETURNS VOID
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
  entity_version BIGINT;
BEGIN
  entity_version := public.next_testagram_replication_version('media', md5(p_object_key));
  INSERT INTO public.platform_replication_outbox
    (event_id, origin, entity_type, entity_id, entity_version, schema_version,
     event_type, occurred_at, payload)
  VALUES
    (gen_random_uuid(), 'testagram-vps', 'media', md5(p_object_key), entity_version, 1,
     p_event_type, now(), COALESCE(p_payload, '{}'::jsonb));
END;
$$;

CREATE OR REPLACE FUNCTION public.replicate_testagram_media_after_write()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public, testagram_media
AS $$
DECLARE
  old_row JSONB;
  new_row JSONB;
  was_public BOOLEAN := FALSE;
  is_public BOOLEAN := FALSE;
  object_key_value TEXT;
  payload JSONB;
  checksum_value TEXT;
BEGIN
  IF TG_OP <> 'INSERT' THEN old_row := to_jsonb(OLD); END IF;
  IF TG_OP <> 'DELETE' THEN new_row := to_jsonb(NEW); END IF;
  object_key_value := COALESCE(new_row->>'object_key', old_row->>'object_key');

  IF old_row IS NOT NULL THEN
    was_public := old_row->>'visibility' = 'public'
      AND old_row->>'status' = 'ready'
      AND old_row->>'checksum_sha256' ~ '^[0-9a-fA-F]{64}$';
  END IF;
  IF new_row IS NOT NULL THEN
    checksum_value := new_row->>'checksum_sha256';
    is_public := new_row->>'visibility' = 'public'
      AND new_row->>'status' = 'ready'
      AND checksum_value ~ '^[0-9a-fA-F]{64}$'
      AND COALESCE(new_row->>'content_type','') IN
        ('image/jpeg','image/png','image/webp','image/gif','image/avif','video/mp4','video/webm')
      AND COALESCE((new_row->>'size_bytes')::BIGINT,0) BETWEEN 1 AND 524288000
      AND object_key_value !~ '(^/|\.\.|[\\?#]|://)';
  END IF;

  -- Private, quarantined, deleted, malformed, and incomplete assets never emit
  -- metadata. A previously public asset transitioning out of public eligibility
  -- emits a tombstone to prevent stale public copies from surviving.
  IF was_public AND NOT is_public THEN
    PERFORM public.enqueue_testagram_public_media_event(object_key_value, 'media.deleted', '{}'::jsonb);
  ELSIF is_public THEN
    payload := jsonb_build_object(
      'id', md5(object_key_value),
      'owner_id', new_row->>'owner_id',
      'object_key', object_key_value,
      'content_type', new_row->>'content_type',
      'size_bytes', (new_row->>'size_bytes')::BIGINT,
      'checksum_sha256', lower(new_row->>'checksum_sha256'),
      'visibility', 'public',
      'created_at', new_row->>'created_at'
    );
    PERFORM public.enqueue_testagram_public_media_event(
      object_key_value,
      CASE WHEN TG_OP = 'INSERT' OR NOT was_public THEN 'media.created' ELSE 'media.updated' END,
      payload
    );
  END IF;

  IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
  RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS testagram_media_replication_insert_delete ON testagram_media.assets;
CREATE TRIGGER testagram_media_replication_insert_delete
AFTER INSERT OR DELETE ON testagram_media.assets
FOR EACH ROW EXECUTE FUNCTION public.replicate_testagram_media_after_write();

DROP TRIGGER IF EXISTS testagram_media_replication_update ON testagram_media.assets;
CREATE TRIGGER testagram_media_replication_update
AFTER UPDATE OF visibility, status, content_type, size_bytes, checksum_sha256, owner_id, storage_namespace, storage_provider
ON testagram_media.assets
FOR EACH ROW EXECUTE FUNCTION public.replicate_testagram_media_after_write();

REVOKE ALL ON FUNCTION public.next_testagram_replication_version(TEXT, TEXT) FROM PUBLIC;
REVOKE ALL ON FUNCTION public.enqueue_testagram_public_media_event(TEXT, TEXT, JSONB) FROM PUBLIC;
REVOKE ALL ON FUNCTION public.replicate_testagram_media_after_write() FROM PUBLIC;

DO $acl$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'anon') THEN
    EXECUTE 'REVOKE ALL ON public.platform_replication_source_versions FROM anon';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'authenticated') THEN
    EXECUTE 'REVOKE ALL ON public.platform_replication_source_versions FROM authenticated';
  END IF;
END;
$acl$;

COMMIT;
