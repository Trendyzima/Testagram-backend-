BEGIN;

-- Older XClone/PostgREST deployments stored media UUIDs in posts.media_ids.
-- Convert any UUID that has a native media asset mapping to its object key
-- before removing the Supabase-only external_media_id bridge.
UPDATE testagram_local.posts p
SET media_ids = COALESCE((
  SELECT jsonb_agg(to_jsonb(COALESCE(a.object_key, item.value)) ORDER BY item.ordinality)
  FROM jsonb_array_elements_text(COALESCE(p.media_ids, '[]'::jsonb))
       WITH ORDINALITY AS item(value, ordinality)
  LEFT JOIN testagram_media.assets a
    ON a.external_media_id::text = item.value
), '[]'::jsonb)
WHERE jsonb_typeof(COALESCE(p.media_ids, '[]'::jsonb)) = 'array';

DROP INDEX IF EXISTS testagram_media.media_assets_external_media_id_uidx;
ALTER TABLE testagram_media.assets DROP COLUMN IF EXISTS external_media_id;

COMMENT ON COLUMN testagram_local.posts.media_ids IS
  'Ordered JSON array of native testagram_media.assets.object_key values; no external media provider IDs.';

COMMIT;
