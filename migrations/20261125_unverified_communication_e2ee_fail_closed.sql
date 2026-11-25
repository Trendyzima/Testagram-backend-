-- Do not treat client-supplied ciphertext as proof of end-to-end encryption.
-- No verified device-key registration, recipient-key verification, recovery,
-- or client-side authenticated-encryption lifecycle is currently enabled.
BEGIN;

UPDATE testagram_local.messages
SET e2ee_enabled = FALSE,
    e2ee_version = NULL,
    e2ee_epoch = NULL
WHERE e2ee_enabled IS DISTINCT FROM FALSE;

ALTER TABLE testagram_local.messages
  ALTER COLUMN e2ee_enabled SET DEFAULT FALSE;

COMMENT ON COLUMN testagram_local.messages.e2ee_enabled IS
  'True only after Testagram device-key registration, recipient-key verification, recovery and client-side authenticated encryption are verified end to end.';

UPDATE testagram_local.call_sessions
SET e2ee_enabled = FALSE
WHERE e2ee_enabled IS DISTINCT FROM FALSE;

ALTER TABLE testagram_local.call_sessions
  ALTER COLUMN e2ee_enabled SET DEFAULT FALSE;

COMMENT ON COLUMN testagram_local.call_sessions.e2ee_enabled IS
  'True only after a verified end-to-end media-key lifecycle is integrated; scoped signaling alone is not media encryption.';

COMMIT;
