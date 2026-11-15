-- Set the canonical VPS logical media pool to 40 GiB.
-- Physical replicas on R2 and B2 are additional provider capacity and are not
-- charged again to the user's logical quota. Refuse to shrink below live usage.
BEGIN;

DO $$
DECLARE
  live_bytes BIGINT;
  target_bytes CONSTANT BIGINT := 42949672960; -- 40 GiB
BEGIN
  SELECT used_bytes + reserved_bytes
    INTO live_bytes
    FROM testagram_storage.pool
   WHERE pool_id = 1
   FOR UPDATE;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'Testagram storage pool row is missing; apply migration 011 first';
  END IF;

  IF live_bytes > target_bytes THEN
    RAISE EXCEPTION 'Cannot set 40 GiB pool: current used + reserved bytes (%) exceed target (%)',
      live_bytes, target_bytes;
  END IF;

  UPDATE testagram_storage.pool
     SET capacity_bytes = target_bytes, updated_at = now()
   WHERE pool_id = 1;
END $$;

COMMENT ON TABLE testagram_storage.pool IS
  'Canonical VPS logical media quota: 40 GiB. Verified replicas on Cloudflare R2 and Backblaze B2 consume separate physical provider capacity.';

COMMIT;
