-- Testagram canonical application data plane on Neon PostgreSQL.
-- Additive foundation only. Does not read, mutate, or delete Appwrite documents.
-- Appwrite account IDs remain immutable text foreign keys.

BEGIN;
CREATE SCHEMA IF NOT EXISTS testagram;

CREATE TABLE IF NOT EXISTS testagram.profiles (
 user_id TEXT PRIMARY KEY,
 handle TEXT NOT NULL,
 display_name TEXT NOT NULL DEFAULT '',
 bio TEXT NOT NULL DEFAULT '',
 avatar_object_id TEXT NOT NULL DEFAULT '',
 profile_visibility TEXT NOT NULL DEFAULT 'public' CHECK (profile_visibility IN ('public','private')),
 federation_enabled BOOLEAN NOT NULL DEFAULT FALSE,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 CONSTRAINT profiles_handle_format CHECK (handle ~ '^[A-Za-z0-9_]{1,30}$')
);
CREATE UNIQUE INDEX IF NOT EXISTS profiles_handle_ci_uq ON testagram.profiles (LOWER(handle));

CREATE TABLE IF NOT EXISTS testagram.posts (
 id TEXT PRIMARY KEY,
 author_id TEXT NOT NULL REFERENCES testagram.profiles(user_id),
 body TEXT NOT NULL DEFAULT '',
 visibility TEXT NOT NULL CHECK (visibility IN ('public','followers','private')),
 status TEXT NOT NULL DEFAULT 'published' CHECK (status IN ('draft','published','deleted')),
 sensitive BOOLEAN NOT NULL DEFAULT FALSE,
 idempotency_key_hash CHAR(64) NOT NULL,
 request_hash CHAR(64) NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 deleted_at TIMESTAMPTZ,
 UNIQUE(author_id, idempotency_key_hash)
);
CREATE INDEX IF NOT EXISTS posts_published_timeline_idx ON testagram.posts (created_at DESC, id DESC) WHERE status='published' AND visibility='public';
CREATE INDEX IF NOT EXISTS posts_author_timeline_idx ON testagram.posts (author_id, created_at DESC, id DESC) WHERE status <> 'deleted';

CREATE TABLE IF NOT EXISTS testagram.follows (
 follower_id TEXT NOT NULL REFERENCES testagram.profiles(user_id),
 followed_id TEXT NOT NULL REFERENCES testagram.profiles(user_id),
 state TEXT NOT NULL DEFAULT 'accepted' CHECK (state IN ('pending','accepted','rejected')),
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY (follower_id, followed_id),
 CHECK (follower_id <> followed_id)
);
CREATE INDEX IF NOT EXISTS follows_followed_state_idx ON testagram.follows (followed_id,state,follower_id);
CREATE INDEX IF NOT EXISTS follows_follower_state_idx ON testagram.follows (follower_id,state,followed_id);

CREATE TABLE IF NOT EXISTS testagram.blocks (
 blocker_id TEXT NOT NULL REFERENCES testagram.profiles(user_id),
 blocked_id TEXT NOT NULL REFERENCES testagram.profiles(user_id),
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY (blocker_id,blocked_id),
 CHECK (blocker_id <> blocked_id)
);
CREATE INDEX IF NOT EXISTS blocks_reverse_idx ON testagram.blocks (blocked_id,blocker_id);

CREATE TABLE IF NOT EXISTS testagram.likes (
 post_id TEXT NOT NULL REFERENCES testagram.posts(id),
 user_id TEXT NOT NULL REFERENCES testagram.profiles(user_id),
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY (post_id,user_id)
);
CREATE INDEX IF NOT EXISTS likes_user_cursor_idx ON testagram.likes (user_id,created_at DESC,post_id);

CREATE TABLE IF NOT EXISTS testagram.comments (
 id TEXT PRIMARY KEY,
 post_id TEXT NOT NULL REFERENCES testagram.posts(id),
 author_id TEXT NOT NULL REFERENCES testagram.profiles(user_id),
 parent_id TEXT REFERENCES testagram.comments(id),
 body TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'published' CHECK (status IN ('published','hidden','deleted')),
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 deleted_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS comments_post_cursor_idx ON testagram.comments (post_id,created_at DESC,id DESC) WHERE status='published';
CREATE INDEX IF NOT EXISTS comments_author_cursor_idx ON testagram.comments (author_id,created_at DESC,id DESC);

CREATE TABLE IF NOT EXISTS testagram.reposts (
 post_id TEXT NOT NULL REFERENCES testagram.posts(id),
 user_id TEXT NOT NULL REFERENCES testagram.profiles(user_id),
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY (post_id,user_id)
);
CREATE INDEX IF NOT EXISTS reposts_user_cursor_idx ON testagram.reposts (user_id,created_at DESC,post_id);

CREATE TABLE IF NOT EXISTS testagram.bookmarks (
 post_id TEXT NOT NULL REFERENCES testagram.posts(id),
 user_id TEXT NOT NULL REFERENCES testagram.profiles(user_id),
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY (post_id,user_id)
);
CREATE INDEX IF NOT EXISTS bookmarks_user_cursor_idx ON testagram.bookmarks (user_id,created_at DESC,post_id);

CREATE TABLE IF NOT EXISTS testagram.backend_outbox (
 event_id TEXT PRIMARY KEY,
 event_type TEXT NOT NULL,
 schema_version INTEGER NOT NULL DEFAULT 1 CHECK (schema_version > 0),
 occurred_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 actor_id TEXT NOT NULL,
 object_id TEXT NOT NULL,
 visibility TEXT NOT NULL CHECK (visibility IN ('public','followers','private')),
 federation_eligible BOOLEAN NOT NULL DEFAULT FALSE,
 payload JSONB NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 claimed_by TEXT,
 lease_until TIMESTAMPTZ,
 attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
 delivered_at TIMESTAMPTZ,
 last_error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS backend_outbox_pending_idx ON testagram.backend_outbox (created_at,event_id) WHERE delivered_at IS NULL;
CREATE INDEX IF NOT EXISTS backend_outbox_federation_idx ON testagram.backend_outbox (created_at,event_id) WHERE federation_eligible=TRUE;

CREATE TABLE IF NOT EXISTS testagram.conversations (
 id TEXT PRIMARY KEY,
 kind TEXT NOT NULL CHECK (kind IN ('direct','group')),
 created_by TEXT NOT NULL REFERENCES testagram.profiles(user_id),
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS testagram.conversation_members (
 conversation_id TEXT NOT NULL REFERENCES testagram.conversations(id),
 user_id TEXT NOT NULL REFERENCES testagram.profiles(user_id),
 joined_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 last_read_at TIMESTAMPTZ,
 PRIMARY KEY (conversation_id,user_id)
);
CREATE INDEX IF NOT EXISTS conversation_members_user_idx ON testagram.conversation_members (user_id,joined_at DESC);

CREATE TABLE IF NOT EXISTS testagram.messages (
 id TEXT PRIMARY KEY,
 conversation_id TEXT NOT NULL REFERENCES testagram.conversations(id),
 sender_id TEXT NOT NULL REFERENCES testagram.profiles(user_id),
 body TEXT NOT NULL DEFAULT '',
 media_object_id TEXT NOT NULL DEFAULT '',
 client_message_id TEXT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 edited_at TIMESTAMPTZ,
 deleted_at TIMESTAMPTZ,
 UNIQUE(sender_id,client_message_id)
);
CREATE INDEX IF NOT EXISTS messages_conversation_cursor_idx ON testagram.messages (conversation_id,created_at DESC,id DESC);

CREATE TABLE IF NOT EXISTS testagram.notifications (
 id TEXT PRIMARY KEY,
 recipient_id TEXT NOT NULL REFERENCES testagram.profiles(user_id),
 actor_id TEXT NOT NULL REFERENCES testagram.profiles(user_id),
 event_type TEXT NOT NULL,
 target_id TEXT NOT NULL,
 read_at TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS notifications_recipient_cursor_idx ON testagram.notifications (recipient_id,created_at DESC,id DESC);
COMMIT;
