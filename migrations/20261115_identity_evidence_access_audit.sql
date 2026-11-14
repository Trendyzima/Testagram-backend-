-- Record every authorized reviewer attempt to retrieve private identity evidence.
-- The API fails closed when this append-only audit event cannot be recorded.
BEGIN;

ALTER TABLE testagram_identity.verification_events
  DROP CONSTRAINT IF EXISTS verification_events_event_type_check;

ALTER TABLE testagram_identity.verification_events
  ADD CONSTRAINT verification_events_event_type_check
  CHECK (event_type IN (
    'session_created','session_started','evidence_submitted','review_requested',
    'decision_verified','decision_failed','decision_escalated','session_expired',
    'session_cancelled','evidence_access_attempted'
  ));

COMMENT ON COLUMN testagram_identity.verification_events.event_type IS
  'Append-only lifecycle and reviewer-access events. evidence_access_attempted is written before private evidence retrieval and contains no raw evidence bytes or storage URL.';

COMMIT;
