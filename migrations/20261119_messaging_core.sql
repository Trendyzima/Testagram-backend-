-- Canonical one-to-one messaging primitives.
BEGIN;
CREATE TABLE IF NOT EXISTS testagram_local.conversations (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 participant_low UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
 participant_high UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
 last_message_at TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 CHECK(participant_low<participant_high),
 UNIQUE(participant_low,participant_high)
);
CREATE INDEX IF NOT EXISTS testagram_conversations_participant_low_idx ON testagram_local.conversations(participant_low,last_message_at DESC NULLS LAST);
CREATE INDEX IF NOT EXISTS testagram_conversations_participant_high_idx ON testagram_local.conversations(participant_high,last_message_at DESC NULLS LAST);
CREATE TABLE IF NOT EXISTS testagram_local.direct_messages (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 conversation_id UUID NOT NULL REFERENCES testagram_local.conversations(id) ON DELETE CASCADE,
 sender_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
 content TEXT NOT NULL CHECK(length(btrim(content)) BETWEEN 1 AND 4000),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 read_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS testagram_direct_messages_timeline_idx ON testagram_local.direct_messages(conversation_id,created_at DESC,id DESC);
DO $grants$
BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.conversations,testagram_local.direct_messages TO testagram_app; END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.conversations,testagram_local.direct_messages TO service_role; END IF;
END;
$grants$;
COMMIT;
