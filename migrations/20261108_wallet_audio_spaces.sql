-- Backend-owned wallets and live audio Spaces on the independent Neon data plane.
-- Provider settlement is deliberately separate: only verified internal credits may fund a wallet.
BEGIN;

CREATE TABLE IF NOT EXISTS testagram_local.wallets (
  user_id UUID PRIMARY KEY REFERENCES testagram_local.accounts(user_id) ON DELETE RESTRICT,
  balance NUMERIC(20,2) NOT NULL DEFAULT 0 CHECK (balance >= 0),
  currency CHAR(3) NOT NULL DEFAULT 'KES',
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','frozen','closed')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS testagram_local.wallet_transfers (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  sender_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE RESTRICT,
  recipient_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE RESTRICT,
  amount NUMERIC(20,2) NOT NULL CHECK (amount > 0 AND amount <= 1000000),
  currency CHAR(3) NOT NULL DEFAULT 'KES',
  kind TEXT NOT NULL DEFAULT 'transfer' CHECK (kind IN ('transfer','tip','superchat')),
  note TEXT NOT NULL DEFAULT '' CHECK (length(note) <= 280),
  idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) BETWEEN 8 AND 128),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (sender_id <> recipient_id),
  UNIQUE(sender_id,idempotency_key)
);
CREATE INDEX IF NOT EXISTS testagram_local_wallet_transfers_sender_idx ON testagram_local.wallet_transfers(sender_id,created_at DESC);
CREATE INDEX IF NOT EXISTS testagram_local_wallet_transfers_recipient_idx ON testagram_local.wallet_transfers(recipient_id,created_at DESC);
CREATE TABLE IF NOT EXISTS testagram_local.wallet_ledger_entries (
  id BIGSERIAL PRIMARY KEY,
  transfer_id UUID NOT NULL REFERENCES testagram_local.wallet_transfers(id) ON DELETE RESTRICT,
  user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE RESTRICT,
  direction TEXT NOT NULL CHECK (direction IN ('debit','credit')),
  amount NUMERIC(20,2) NOT NULL CHECK (amount > 0),
  currency CHAR(3) NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(transfer_id,user_id,direction)
);
CREATE TABLE IF NOT EXISTS testagram_local.wallet_transactions (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE RESTRICT,
  transfer_id UUID NOT NULL REFERENCES testagram_local.wallet_transfers(id) ON DELETE RESTRICT,
  kind TEXT NOT NULL CHECK (kind IN ('transfer_out','transfer_in','tip_out','tip_in','superchat_out','superchat_in')),
  amount NUMERIC(20,2) NOT NULL CHECK (amount > 0),
  currency CHAR(3) NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(user_id,transfer_id,kind)
);
CREATE INDEX IF NOT EXISTS testagram_local_wallet_transactions_user_idx ON testagram_local.wallet_transactions(user_id,created_at DESC,id DESC);

CREATE TABLE IF NOT EXISTS testagram_local.audio_spaces (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  host_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE RESTRICT,
  title TEXT NOT NULL CHECK (length(btrim(title)) BETWEEN 1 AND 120),
  description TEXT NOT NULL DEFAULT '' CHECK (length(description)<=2000),
  status TEXT NOT NULL DEFAULT 'scheduled' CHECK (status IN ('scheduled','live','ended','cancelled')),
  scheduled_at TIMESTAMPTZ,
  started_at TIMESTAMPTZ,
  ended_at TIMESTAMPTZ,
  max_audience INTEGER NOT NULL DEFAULT 1000 CHECK (max_audience BETWEEN 2 AND 10000),
  listener_count INTEGER NOT NULL DEFAULT 0 CHECK (listener_count>=0),
  listener_requests_enabled BOOLEAN NOT NULL DEFAULT TRUE,
  recording_media_id TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS testagram_local_audio_spaces_live_idx ON testagram_local.audio_spaces(listener_count DESC,started_at DESC) WHERE status='live';
CREATE INDEX IF NOT EXISTS testagram_local_audio_spaces_host_idx ON testagram_local.audio_spaces(host_id,created_at DESC);
CREATE TABLE IF NOT EXISTS testagram_local.audio_space_participants (
  space_id UUID NOT NULL REFERENCES testagram_local.audio_spaces(id) ON DELETE CASCADE,
  user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  role TEXT NOT NULL DEFAULT 'listener' CHECK (role IN ('host','speaker','listener')),
  joined_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  left_at TIMESTAMPTZ,
  PRIMARY KEY(space_id,user_id)
);
CREATE INDEX IF NOT EXISTS testagram_local_audio_participants_active_idx ON testagram_local.audio_space_participants(space_id,joined_at) WHERE left_at IS NULL;
CREATE TABLE IF NOT EXISTS testagram_local.audio_space_speaker_requests (
  space_id UUID NOT NULL REFERENCES testagram_local.audio_spaces(id) ON DELETE CASCADE,
  user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE CASCADE,
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','denied')),
  requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  decided_at TIMESTAMPTZ,
  decided_by UUID REFERENCES testagram_local.accounts(user_id),
  PRIMARY KEY(space_id,user_id)
);
CREATE TABLE IF NOT EXISTS testagram_local.audio_space_superchats (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  space_id UUID NOT NULL REFERENCES testagram_local.audio_spaces(id) ON DELETE CASCADE,
  sender_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE RESTRICT,
  transfer_id UUID NOT NULL UNIQUE REFERENCES testagram_local.wallet_transfers(id) ON DELETE RESTRICT,
  message TEXT NOT NULL CHECK (length(btrim(message)) BETWEEN 1 AND 280),
  amount NUMERIC(20,2) NOT NULL CHECK (amount > 0),
  pinned_until TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS testagram_local_audio_superchat_live_idx ON testagram_local.audio_space_superchats(space_id,pinned_until DESC) WHERE pinned_until>now();

REVOKE ALL ON testagram_local.wallets,testagram_local.wallet_transfers,testagram_local.wallet_ledger_entries,
 testagram_local.wallet_transactions,testagram_local.audio_spaces,testagram_local.audio_space_participants,
 testagram_local.audio_space_speaker_requests,testagram_local.audio_space_superchats FROM PUBLIC;
DO $grants$
BEGIN
 IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='testagram_app') THEN
  GRANT SELECT,INSERT,UPDATE ON testagram_local.wallets TO testagram_app;
  GRANT SELECT,INSERT ON testagram_local.wallet_transfers,testagram_local.wallet_ledger_entries,testagram_local.wallet_transactions TO testagram_app;
  GRANT SELECT,INSERT,UPDATE ON testagram_local.audio_spaces,testagram_local.audio_space_participants,testagram_local.audio_space_speaker_requests,testagram_local.audio_space_superchats TO testagram_app;
  GRANT USAGE,SELECT ON SEQUENCE testagram_local.wallet_ledger_entries_id_seq TO testagram_app;
 END IF;
 IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='service_role') THEN
  GRANT SELECT,INSERT,UPDATE ON testagram_local.wallets TO service_role;
  GRANT SELECT,INSERT ON testagram_local.wallet_transfers,testagram_local.wallet_ledger_entries,testagram_local.wallet_transactions TO service_role;
  GRANT SELECT,INSERT,UPDATE ON testagram_local.audio_spaces,testagram_local.audio_space_participants,testagram_local.audio_space_speaker_requests,testagram_local.audio_space_superchats TO service_role;
  GRANT USAGE,SELECT ON SEQUENCE testagram_local.wallet_ledger_entries_id_seq TO service_role;
 END IF;
END $grants$;
COMMIT;
