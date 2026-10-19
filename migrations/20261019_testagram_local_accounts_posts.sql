-- Local authoritative account/profile and post records for Testagram.
-- Apply after 20261019_dual_backend_replication.sql. Writes remain feature-gated
-- until the application and authorization integration is explicitly enabled.
BEGIN;

CREATE SCHEMA IF NOT EXISTS testagram_local;

CREATE TABLE IF NOT EXISTS testagram_local.accounts (
  user_id UUID PRIMARY KEY,
  username TEXT NOT NULL CHECK (username ~ '^[A-Za-z0-9_]{3,30}$'),
  display_name TEXT NOT NULL DEFAULT '' CHECK (length(display_name) <= 80),
  bio TEXT NOT NULL DEFAULT '' CHECK (length(bio) <= 500),
  website TEXT NOT NULL DEFAULT '' CHECK (length(website) <= 2048),
  avatar_key TEXT NOT NULL DEFAULT '' CHECK (length(avatar_key) <= 512),
  discoverable_by_username BOOLEAN NOT NULL DEFAULT TRUE,
  protected_account BOOLEAN NOT NULL DEFAULT FALSE,
  account_status TEXT NOT NULL DEFAULT 'active' CHECK (account_status IN ('active','deactivated','suspended')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS testagram_local_accounts_username_ci
  ON testagram_local.accounts (lower(username));

CREATE TABLE IF NOT EXISTS testagram_local.posts (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  author_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE RESTRICT,
  text TEXT NOT NULL DEFAULT '' CHECK (length(text) <= 10000),
  visibility TEXT NOT NULL DEFAULT 'public' CHECK (visibility IN ('public','followers','private')),
  media_ids JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(media_ids) = 'array' AND octet_length(media_ids::text) <= 16384),
  language TEXT NOT NULL DEFAULT '' CHECK (length(language) <= 32),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS testagram_local_posts_author_created
  ON testagram_local.posts(author_id, created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS testagram_local_posts_public_created
  ON testagram_local.posts(created_at DESC) WHERE deleted_at IS NULL AND visibility='public';

CREATE TABLE IF NOT EXISTS public.platform_replication_source_versions (
  origin TEXT NOT NULL CHECK (origin = 'testagram-vps'),
  entity_type TEXT NOT NULL CHECK (entity_type ~ '^[a-z][a-z0-9_-]{0,31}$'),
  entity_id TEXT NOT NULL CHECK (length(entity_id) BETWEEN 1 AND 128),
  current_version BIGINT NOT NULL CHECK (current_version > 0),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (origin, entity_type, entity_id)
);

CREATE OR REPLACE FUNCTION public.next_testagram_replication_version(
  p_entity_type TEXT, p_entity_id TEXT
) RETURNS BIGINT
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE next_version BIGINT;
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

CREATE OR REPLACE FUNCTION testagram_local.emit_public_account_event()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, public, testagram_local
AS $$
DECLARE old_public BOOLEAN := FALSE; new_public BOOLEAN := FALSE; v BIGINT; payload JSONB;
BEGIN
  IF TG_OP <> 'INSERT' THEN
    old_public := OLD.account_status='active' AND OLD.discoverable_by_username AND NOT OLD.protected_account;
  END IF;
  IF TG_OP <> 'DELETE' THEN
    new_public := NEW.account_status='active' AND NEW.discoverable_by_username AND NOT NEW.protected_account;
  END IF;
  IF old_public AND NOT new_public THEN
    v := public.next_testagram_replication_version('account', OLD.user_id::text);
    INSERT INTO public.platform_replication_outbox(event_id,origin,entity_type,entity_id,entity_version,schema_version,event_type,occurred_at,payload)
    VALUES(gen_random_uuid(),'testagram-vps','account',OLD.user_id::text,v,1,'account.deleted',now(),'{}'::jsonb);
  ELSIF new_public THEN
    payload := jsonb_build_object('id',NEW.user_id::text,'username',NEW.username,'display_name',NEW.display_name,
      'bio',NEW.bio,'avatar_key',NULLIF(NEW.avatar_key,''),'website',NULLIF(NEW.website,''));
    v := public.next_testagram_replication_version('account', NEW.user_id::text);
    INSERT INTO public.platform_replication_outbox(event_id,origin,entity_type,entity_id,entity_version,schema_version,event_type,occurred_at,payload)
    VALUES(gen_random_uuid(),'testagram-vps','account',NEW.user_id::text,v,1,
      CASE WHEN TG_OP='INSERT' OR NOT old_public THEN 'account.created' ELSE 'account.updated' END,now(),payload);
  END IF;
  -- Re-evaluate every public post when account privacy/status changes. The post
  -- trigger sees the committed account state and emits tombstones if needed.
  IF TG_OP='UPDATE' AND old_public IS DISTINCT FROM new_public THEN
    UPDATE testagram_local.posts SET updated_at=now()
    WHERE author_id=NEW.user_id AND visibility='public' AND deleted_at IS NULL;
  END IF;
  IF TG_OP='DELETE' THEN RETURN OLD; END IF;
  RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION testagram_local.emit_public_post_event()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, public, testagram_local
AS $$
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
      'visibility','public','created_at',NEW.created_at,'updated_at',NEW.updated_at,
      'media_ids',NEW.media_ids,'language',NEW.language);
    v := public.next_testagram_replication_version('post', NEW.id::text);
    INSERT INTO public.platform_replication_outbox(event_id,origin,entity_type,entity_id,entity_version,schema_version,event_type,occurred_at,payload)
    VALUES(gen_random_uuid(),'testagram-vps','post',NEW.id::text,v,1,
      CASE WHEN TG_OP='INSERT' OR NOT old_public THEN 'post.created' ELSE 'post.updated' END,now(),payload);
  END IF;
  IF TG_OP='DELETE' THEN RETURN OLD; END IF;
  RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS testagram_local_account_replication ON testagram_local.accounts;
CREATE TRIGGER testagram_local_account_replication
AFTER INSERT OR UPDATE OR DELETE ON testagram_local.accounts
FOR EACH ROW EXECUTE FUNCTION testagram_local.emit_public_account_event();

DROP TRIGGER IF EXISTS testagram_local_post_replication ON testagram_local.posts;
CREATE TRIGGER testagram_local_post_replication
AFTER INSERT OR UPDATE OR DELETE ON testagram_local.posts
FOR EACH ROW EXECUTE FUNCTION testagram_local.emit_public_post_event();

REVOKE ALL ON SCHEMA testagram_local FROM PUBLIC;
REVOKE ALL ON testagram_local.accounts, testagram_local.posts, public.platform_replication_source_versions FROM PUBLIC;
REVOKE ALL ON FUNCTION public.next_testagram_replication_version(TEXT,TEXT) FROM PUBLIC;
REVOKE ALL ON FUNCTION testagram_local.emit_public_account_event() FROM PUBLIC;
REVOKE ALL ON FUNCTION testagram_local.emit_public_post_event() FROM PUBLIC;

DO $acl$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
    EXECUTE 'GRANT USAGE ON SCHEMA testagram_local TO service_role';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON testagram_local.accounts, testagram_local.posts TO service_role';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON public.platform_replication_source_versions TO service_role';
    EXECUTE 'GRANT EXECUTE ON FUNCTION public.next_testagram_replication_version(TEXT,TEXT) TO service_role';
  END IF;
END;
$acl$;

COMMIT;
