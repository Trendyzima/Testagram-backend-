-- Backend-owned community chat, reactions and moderation pins.
BEGIN;
CREATE TABLE IF NOT EXISTS testagram_local.community_chat_messages (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  community_id UUID NOT NULL REFERENCES testagram_local.communities(id) ON DELETE CASCADE,
  user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE RESTRICT,
  message TEXT NOT NULL CHECK (length(btrim(message)) BETWEEN 1 AND 2000),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS testagram_community_chat_timeline_idx
  ON testagram_local.community_chat_messages(community_id,created_at,id)
  WHERE deleted_at IS NULL;
CREATE TABLE IF NOT EXISTS testagram_local.community_chat_reactions (
  message_id UUID NOT NULL REFERENCES testagram_local.community_chat_messages(id) ON DELETE CASCADE,
  user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  emoji TEXT NOT NULL CHECK (length(emoji) BETWEEN 1 AND 16),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY(message_id,user_id,emoji)
);
CREATE INDEX IF NOT EXISTS testagram_community_chat_reaction_message_idx
  ON testagram_local.community_chat_reactions(message_id,emoji);
CREATE TABLE IF NOT EXISTS testagram_local.community_chat_pins (
  community_id UUID NOT NULL REFERENCES testagram_local.communities(id) ON DELETE CASCADE,
  message_id UUID NOT NULL REFERENCES testagram_local.community_chat_messages(id) ON DELETE CASCADE,
  pinned_by UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE RESTRICT,
  pinned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY(community_id,message_id)
);
REVOKE ALL ON testagram_local.community_chat_messages,testagram_local.community_chat_reactions,testagram_local.community_chat_pins FROM PUBLIC;
DO $grants$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
    GRANT USAGE ON SCHEMA testagram_local TO testagram_app;
    GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.community_chat_messages,testagram_local.community_chat_reactions,testagram_local.community_chat_pins TO testagram_app;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
    GRANT USAGE ON SCHEMA testagram_local TO service_role;
    GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.community_chat_messages,testagram_local.community_chat_reactions,testagram_local.community_chat_pins TO service_role;
  END IF;
END;
$grants$;
COMMIT;
