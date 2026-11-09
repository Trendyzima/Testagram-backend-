-- Native post creation metadata and per-author idempotency.
-- Apply after 20261019_testagram_local_accounts_posts.sql.
BEGIN;

ALTER TABLE testagram_local.posts
  ADD COLUMN IF NOT EXISTS sensitive BOOLEAN NOT NULL DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS idempotency_key TEXT;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname = 'testagram_local_posts_idempotency_key_length'
      AND conrelid = 'testagram_local.posts'::regclass
  ) THEN
    ALTER TABLE testagram_local.posts
      ADD CONSTRAINT testagram_local_posts_idempotency_key_length
      CHECK (idempotency_key IS NULL OR length(idempotency_key) BETWEEN 8 AND 128);
  END IF;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS testagram_local_posts_author_idempotency
  ON testagram_local.posts(author_id,idempotency_key)
  WHERE idempotency_key IS NOT NULL;

REVOKE ALL ON testagram_local.posts FROM PUBLIC;


-- Carry the sensitive-content marker through the existing transactional
-- replication outbox so downstream feed projections do not lose the label.
CREATE OR REPLACE FUNCTION testagram_local.emit_public_post_event()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, public, testagram_local
AS $
DECLARE author_public BOOLEAN := FALSE; old_public BOOLEAN := FALSE; new_public BOOLEAN := FALSE; v BIGINT; payload JSONB;
BEGIN
  IF TG_OP <> 'INSERT' THEN
    old_public := OLD.visibility='public' AND OLD.deleted_at IS NULL;
  END IF;
  IF TG_OP <> 'DELETE' THEN
    SELECT account_status='active' AND discoverable_by_username AND NOT protected_account
      INTO author_public FROM testagram_local.accounts WHERE user_id=NEW.author_id;
    new_public := NEW.visibility='public' AND NEW.deleted_at IS NULL AND COALESCE(author_public,FALSE);
  END IF;
  IF old_public AND NOT new_public THEN
    v := public.next_testagram_replication_version('post', OLD.id::text);
    INSERT INTO public.platform_replication_outbox(event_id,origin,entity_type,entity_id,entity_version,schema_version,event_type,occurred_at,payload)
    VALUES(gen_random_uuid(),'testagram-vps','post',OLD.id::text,v,1,'post.deleted',now(),'{}'::jsonb);
  ELSIF new_public THEN
    payload := jsonb_build_object('id',NEW.id::text,'author_id',NEW.author_id::text,'text',NEW.text,
      'visibility','public','sensitive',NEW.sensitive,'created_at',NEW.created_at,'updated_at',NEW.updated_at,
      'media_ids',NEW.media_ids,'language',NEW.language);
    v := public.next_testagram_replication_version('post', NEW.id::text);
    INSERT INTO public.platform_replication_outbox(event_id,origin,entity_type,entity_id,entity_version,schema_version,event_type,occurred_at,payload)
    VALUES(gen_random_uuid(),'testagram-vps','post',NEW.id::text,v,1,
      CASE WHEN TG_OP='INSERT' OR NOT old_public THEN 'post.created' ELSE 'post.updated' END,now(),payload);
  END IF;
  IF TG_OP='DELETE' THEN RETURN OLD; END IF;
  RETURN NEW;
END;
$;

COMMIT;
