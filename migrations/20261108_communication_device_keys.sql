-- Appwrite-authenticated device identity and per-conversation E2EE key envelopes.
-- Private keys remain in browser/device secure storage; only public keys and
-- recipient-specific encrypted key envelopes are persisted by Backend.
BEGIN;
CREATE TABLE IF NOT EXISTS testagram_local.communication_devices (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  device_id TEXT NOT NULL CHECK (length(device_id) BETWEEN 8 AND 128),
  identity_public_key TEXT NOT NULL CHECK (length(identity_public_key) BETWEEN 64 AND 8192),
  last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  revoked_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(user_id, device_id)
);
CREATE INDEX IF NOT EXISTS communication_devices_active_user_idx
  ON testagram_local.communication_devices(user_id,last_seen_at DESC) WHERE revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS testagram_local.conversation_key_envelopes (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  conversation_id UUID NOT NULL REFERENCES testagram_local.conversations(id) ON DELETE CASCADE,
  epoch BIGINT NOT NULL CHECK (epoch BETWEEN 1 AND 1000000),
  sender_device_id UUID NOT NULL REFERENCES testagram_local.communication_devices(id) ON DELETE CASCADE,
  recipient_device_id UUID NOT NULL REFERENCES testagram_local.communication_devices(id) ON DELETE CASCADE,
  ciphertext TEXT NOT NULL CHECK (length(ciphertext) BETWEEN 16 AND 16384),
  nonce TEXT NOT NULL CHECK (length(nonce) BETWEEN 8 AND 256),
  salt TEXT NOT NULL CHECK (length(salt) BETWEEN 8 AND 256),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(conversation_id,epoch,recipient_device_id),
  CHECK (sender_device_id <> recipient_device_id)
);
CREATE INDEX IF NOT EXISTS conversation_key_envelopes_recipient_idx
  ON testagram_local.conversation_key_envelopes(recipient_device_id,conversation_id,epoch DESC);

REVOKE ALL ON testagram_local.communication_devices,testagram_local.conversation_key_envelopes FROM PUBLIC;
DO $grants$
BEGIN
 IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
  GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.communication_devices,testagram_local.conversation_key_envelopes TO testagram_app;
 END IF;
 IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
  GRANT SELECT,INSERT,UPDATE,DELETE ON testagram_local.communication_devices,testagram_local.conversation_key_envelopes TO service_role;
 END IF;
END $grants$;
COMMIT;
