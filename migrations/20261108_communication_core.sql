-- Native communication and call state on the independent Neon social data plane.
-- Appwrite identity is resolved by Backend before any query reaches these tables.
BEGIN;

CREATE TABLE IF NOT EXISTS testagram_local.conversations (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  kind TEXT NOT NULL CHECK (kind IN ('direct','group')),
  created_by UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  title TEXT NOT NULL DEFAULT '' CHECK (length(title) <= 120),
  encrypted BOOLEAN NOT NULL DEFAULT TRUE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS testagram_local.conversation_members (
  conversation_id UUID NOT NULL REFERENCES testagram_local.conversations(id) ON DELETE CASCADE,
  user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  role TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('owner','admin','member')),
  joined_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  left_at TIMESTAMPTZ,
  last_read_at TIMESTAMPTZ,
  archived_at TIMESTAMPTZ,
  pinned_at TIMESTAMPTZ,
  muted_until TIMESTAMPTZ,
  disappearing_seconds INTEGER CHECK (disappearing_seconds IS NULL OR disappearing_seconds BETWEEN 0 AND 2592000),
  PRIMARY KEY (conversation_id,user_id)
);
CREATE INDEX IF NOT EXISTS testagram_local_conversation_members_user_idx
  ON testagram_local.conversation_members(user_id,joined_at DESC) WHERE left_at IS NULL;

CREATE TABLE IF NOT EXISTS testagram_local.messages (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  conversation_id UUID NOT NULL REFERENCES testagram_local.conversations(id) ON DELETE CASCADE,
  sender_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  body TEXT NOT NULL DEFAULT '',
  client_message_id TEXT,
  reply_to_message_id UUID REFERENCES testagram_local.messages(id) ON DELETE SET NULL,
  shared_post_id UUID REFERENCES testagram_local.posts(id) ON DELETE SET NULL,
  ciphertext TEXT,
  nonce TEXT,
  aad TEXT,
  e2ee_enabled BOOLEAN NOT NULL DEFAULT FALSE,
  e2ee_version TEXT,
  e2ee_epoch BIGINT,
  attachment_metadata JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(attachment_metadata)='array' AND octet_length(attachment_metadata::text)<=16384),
  media_url TEXT,
  media_type TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  edited_at TIMESTAMPTZ,
  deleted_at TIMESTAMPTZ,
  delivered_at TIMESTAMPTZ,
  read_at TIMESTAMPTZ,
  UNIQUE(sender_id,client_message_id),
  CHECK ((e2ee_enabled=FALSE) OR (ciphertext IS NOT NULL AND nonce IS NOT NULL AND e2ee_epoch IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS testagram_local_messages_conversation_cursor_idx
  ON testagram_local.messages(conversation_id,created_at DESC,id DESC) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS testagram_local_messages_sender_cursor_idx
  ON testagram_local.messages(sender_id,created_at DESC,id DESC);

CREATE TABLE IF NOT EXISTS testagram_local.message_reactions (
  message_id UUID NOT NULL REFERENCES testagram_local.messages(id) ON DELETE CASCADE,
  user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  emoji TEXT NOT NULL CHECK (length(emoji) BETWEEN 1 AND 32),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY(message_id,user_id)
);
CREATE TABLE IF NOT EXISTS testagram_local.message_stars (
  message_id UUID NOT NULL REFERENCES testagram_local.messages(id) ON DELETE CASCADE,
  user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY(message_id,user_id)
);

CREATE TABLE IF NOT EXISTS testagram_local.call_sessions (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  conversation_id UUID NOT NULL REFERENCES testagram_local.conversations(id) ON DELETE CASCADE,
  created_by UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  provider TEXT NOT NULL DEFAULT 'testagram-p2p' CHECK (provider IN ('testagram-p2p','livekit')),
  room_name TEXT NOT NULL UNIQUE,
  kind TEXT NOT NULL DEFAULT 'video' CHECK (kind IN ('voice','video','screen')),
  status TEXT NOT NULL DEFAULT 'ringing' CHECK (status IN ('ringing','active','ended','failed')),
  e2ee_enabled BOOLEAN NOT NULL DEFAULT TRUE,
  e2ee_epoch BIGINT NOT NULL DEFAULT 1,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata)='object' AND octet_length(metadata::text)<=8192),
  started_at TIMESTAMPTZ,
  ended_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS testagram_local_call_sessions_conversation_idx
  ON testagram_local.call_sessions(conversation_id,created_at DESC);
CREATE TABLE IF NOT EXISTS testagram_local.call_participants (
  call_id UUID NOT NULL REFERENCES testagram_local.call_sessions(id) ON DELETE CASCADE,
  user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  joined_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  left_at TIMESTAMPTZ,
  PRIMARY KEY(call_id,user_id)
);

CREATE TABLE IF NOT EXISTS testagram_local.communication_outbox (
  id BIGSERIAL PRIMARY KEY,
  event_key TEXT NOT NULL UNIQUE,
  event_type TEXT NOT NULL,
  conversation_id UUID NOT NULL REFERENCES testagram_local.conversations(id) ON DELETE CASCADE,
  message_id UUID REFERENCES testagram_local.messages(id) ON DELETE CASCADE,
  payload JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(payload)='object' AND octet_length(payload::text)<=32768),
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','processing','sent','failed','dead')),
  attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts>=0),
  next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  claimed_by TEXT,
  lease_until TIMESTAMPTZ,
  last_error TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  sent_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS testagram_local_communication_outbox_pending_idx
  ON testagram_local.communication_outbox(next_attempt_at,created_at) WHERE status IN ('pending','failed');

-- Message notifications are generated transactionally without exposing plaintext.
CREATE OR REPLACE FUNCTION testagram_local.notify_conversation_members()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,testagram_local AS $$
BEGIN
  INSERT INTO testagram_local.notifications(recipient_id,actor_id,kind,payload,event_key)
  SELECT cm.user_id,NEW.sender_id,'message.received',
    jsonb_build_object('message_id',NEW.id,'conversation_id',NEW.conversation_id,'encrypted',NEW.e2ee_enabled),
    'message:'||NEW.id::text
  FROM testagram_local.conversation_members cm
  WHERE cm.conversation_id=NEW.conversation_id AND cm.user_id<>NEW.sender_id AND cm.left_at IS NULL
  ON CONFLICT(recipient_id,event_key) WHERE event_key IS NOT NULL DO NOTHING;
  RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS testagram_local_message_notifications ON testagram_local.messages;
CREATE TRIGGER testagram_local_message_notifications AFTER INSERT ON testagram_local.messages
  FOR EACH ROW EXECUTE FUNCTION testagram_local.notify_conversation_members();
REVOKE ALL ON FUNCTION testagram_local.notify_conversation_members() FROM PUBLIC;

REVOKE ALL ON testagram_local.conversations,testagram_local.conversation_members,testagram_local.messages,
  testagram_local.message_reactions,testagram_local.message_stars,testagram_local.call_sessions,
  testagram_local.call_participants,testagram_local.communication_outbox FROM PUBLIC;
DO $grants$
BEGIN
 IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
  GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.conversations,testagram_local.conversation_members,
   testagram_local.messages,testagram_local.message_reactions,testagram_local.message_stars,
   testagram_local.call_sessions,testagram_local.call_participants TO testagram_app;
  GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.communication_outbox TO testagram_app;
  GRANT USAGE,SELECT ON SEQUENCE testagram_local.communication_outbox_id_seq TO testagram_app;
 END IF;
 IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
  GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.conversations,testagram_local.conversation_members,
   testagram_local.messages,testagram_local.message_reactions,testagram_local.message_stars,
   testagram_local.call_sessions,testagram_local.call_participants,testagram_local.communication_outbox TO service_role;
  GRANT USAGE,SELECT ON SEQUENCE testagram_local.communication_outbox_id_seq TO service_role;
 END IF;
END $grants$;
COMMIT;
