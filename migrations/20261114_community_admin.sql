-- Community settings and pinned post metadata.
BEGIN;
ALTER TABLE testagram_local.communities
 ADD COLUMN IF NOT EXISTS pinned_post_ids JSONB NOT NULL DEFAULT '[]'::jsonb
 CHECK(jsonb_typeof(pinned_post_ids)='array' AND jsonb_array_length(pinned_post_ids)<=3);
COMMIT;
