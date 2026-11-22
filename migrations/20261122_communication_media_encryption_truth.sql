-- Live audio/video media encryption is not yet implemented by the Flutter
-- client. Never advertise a call session as end-to-end encrypted merely
-- because the signaling ticket is scoped to a room.
UPDATE testagram_local.call_sessions
SET e2ee_enabled = FALSE
WHERE e2ee_enabled IS DISTINCT FROM FALSE;

ALTER TABLE testagram_local.call_sessions
  ALTER COLUMN e2ee_enabled SET DEFAULT FALSE;

COMMENT ON COLUMN testagram_local.call_sessions.e2ee_enabled IS
  'True only after a verified media E2EE lifecycle is integrated; scoped signaling alone is not media encryption.';
