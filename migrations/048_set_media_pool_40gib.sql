-- Enforce the operator-approved 40 GiB logical media pool.
-- This is additive and deliberately refuses to shrink below already charged usage.
-- It does not delete objects, change provider routing, or provision physical disks.
BEGIN;

DO $$
DECLARE
  target_bytes CONSTANT bigint := 42949672960; -- 40 GiB
  current_used bigint;
  current_reserved bigint;
BEGIN
  SELECT used_bytes, reserved_bytes
    INTO current_used, current_reserved
    FROM testagram_storage.pool
   WHERE pool_id = 1
   FOR UPDATE;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'TESTAGRAM_STORAGE_POOL_NOT_INITIALIZED';
  END IF;

  IF current_used + current_reserved > target_bytes THEN
    RAISE EXCEPTION
      'TESTAGRAM_STORAGE_CAPACITY_BELOW_CURRENT_COMMITMENTS: used=% reserved=% target=%',
      current_used, current_reserved, target_bytes;
  END IF;

  UPDATE testagram_storage.pool
     SET capacity_bytes = target_bytes,
         updated_at = now()
   WHERE pool_id = 1;
END $$;

COMMENT ON TABLE testagram_storage.pool IS
  'Canonical logical media quota pool. Physical VPS/R2/B2 capacities and replicas are tracked separately.';
COMMENT ON COLUMN testagram_storage.pool.capacity_bytes IS
  'Configured logical capacity in bytes; 40 GiB after migration 048. This does not provision physical storage.';

COMMIT;
