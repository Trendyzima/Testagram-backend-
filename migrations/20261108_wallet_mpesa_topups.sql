-- Verified Safaricom STK top-ups. Provider callbacks never credit a wallet
-- until Backend independently queries Safaricom and settles exactly once.
BEGIN;

CREATE TABLE IF NOT EXISTS testagram_local.wallet_topups (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL REFERENCES testagram_local.accounts(user_id) ON DELETE RESTRICT,
  amount_kes NUMERIC(20,2) NOT NULL CHECK (amount_kes BETWEEN 10 AND 150000),
  wallet_amount NUMERIC(20,2) NOT NULL CHECK (wallet_amount > 0),
  wallet_currency CHAR(3) NOT NULL CHECK (wallet_currency IN ('KES','USD')),
  fx_rate NUMERIC(14,6) NOT NULL DEFAULT 1 CHECK (fx_rate > 0),
  phone_e164 TEXT NOT NULL CHECK (phone_e164 ~ '^254[17][0-9]{8}$'),
  idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) BETWEEN 8 AND 128),
  request_reference TEXT NOT NULL UNIQUE,
  checkout_request_id TEXT UNIQUE,
  merchant_request_id TEXT,
  receipt_number TEXT UNIQUE,
  status TEXT NOT NULL DEFAULT 'initiating' CHECK (status IN ('initiating','pending','settled','failed')),
  result_code INTEGER,
  result_description TEXT NOT NULL DEFAULT '',
  provider_response JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(provider_response)='object' AND octet_length(provider_response::text)<=16384),
  callback_data JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(callback_data)='object' AND octet_length(callback_data::text)<=16384),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  settled_at TIMESTAMPTZ,
  UNIQUE(user_id,idempotency_key)
);
CREATE INDEX IF NOT EXISTS testagram_local_wallet_topups_user_idx ON testagram_local.wallet_topups(user_id,created_at DESC);

ALTER TABLE testagram_local.wallet_transactions ALTER COLUMN transfer_id DROP NOT NULL;
ALTER TABLE testagram_local.wallet_transactions ADD COLUMN IF NOT EXISTS topup_id UUID REFERENCES testagram_local.wallet_topups(id) ON DELETE RESTRICT;
ALTER TABLE testagram_local.wallet_transactions ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'completed' CHECK (status IN ('pending','completed','failed'));
ALTER TABLE testagram_local.wallet_transactions ADD COLUMN IF NOT EXISTS provider TEXT NOT NULL DEFAULT '';
ALTER TABLE testagram_local.wallet_transactions ADD COLUMN IF NOT EXISTS provider_order_id TEXT NOT NULL DEFAULT '';
ALTER TABLE testagram_local.wallet_transactions ADD COLUMN IF NOT EXISTS provider_reference TEXT NOT NULL DEFAULT '';
ALTER TABLE testagram_local.wallet_transactions ADD COLUMN IF NOT EXISTS provider_status TEXT NOT NULL DEFAULT '';
ALTER TABLE testagram_local.wallet_transactions ADD COLUMN IF NOT EXISTS metadata JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata)='object' AND octet_length(metadata::text)<=16384);
ALTER TABLE testagram_local.wallet_transactions DROP CONSTRAINT IF EXISTS wallet_transactions_kind_check;
ALTER TABLE testagram_local.wallet_transactions ADD CONSTRAINT wallet_transactions_kind_check
  CHECK (kind IN ('transfer_out','transfer_in','tip_out','tip_in','superchat_out','superchat_in','deposit','withdrawal'));
ALTER TABLE testagram_local.wallet_transactions ADD CONSTRAINT wallet_transactions_transfer_or_topup_check
  CHECK ((transfer_id IS NOT NULL AND topup_id IS NULL) OR (transfer_id IS NULL AND topup_id IS NOT NULL));
CREATE UNIQUE INDEX IF NOT EXISTS testagram_local_wallet_transactions_topup_uidx ON testagram_local.wallet_transactions(user_id,topup_id) WHERE topup_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS testagram_local_wallet_transactions_provider_idx ON testagram_local.wallet_transactions(provider,provider_order_id) WHERE provider_order_id<>'';

ALTER TABLE testagram_local.wallet_ledger_entries ALTER COLUMN transfer_id DROP NOT NULL;
ALTER TABLE testagram_local.wallet_ledger_entries ADD COLUMN IF NOT EXISTS topup_id UUID REFERENCES testagram_local.wallet_topups(id) ON DELETE RESTRICT;
ALTER TABLE testagram_local.wallet_ledger_entries ADD CONSTRAINT wallet_ledger_transfer_or_topup_check
  CHECK ((transfer_id IS NOT NULL AND topup_id IS NULL) OR (transfer_id IS NULL AND topup_id IS NOT NULL));
CREATE UNIQUE INDEX IF NOT EXISTS testagram_local_wallet_ledger_topup_uidx ON testagram_local.wallet_ledger_entries(topup_id,user_id,direction) WHERE topup_id IS NOT NULL;

GRANT SELECT,INSERT,UPDATE ON testagram_local.wallet_topups TO testagram_app,service_role;
GRANT SELECT,INSERT,UPDATE ON testagram_local.wallet_transactions,testagram_local.wallet_ledger_entries TO testagram_app,service_role;
COMMIT;
