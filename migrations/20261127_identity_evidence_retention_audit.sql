-- Append-only audit events for identity-evidence retention and physical deletion.
BEGIN;

ALTER TABLE testagram_identity.verification_events
  DROP CONSTRAINT IF EXISTS verification_events_event_type_check;

ALTER TABLE testagram_identity.verification_events
  ADD CONSTRAINT verification_events_event_type_check
  CHECK (event_type IN (
    'session_created','session_started','evidence_submitted','review_requested',
    'decision_verified','decision_failed','decision_escalated','session_expired',
    'session_cancelled','evidence_access_attempted','account_provisioned',
    'email_verified','identity_evidence_quarantined','identity_evidence_deleted'
  ));

COMMENT ON COLUMN testagram_identity.verification_events.event_type IS
  'Append-only identity lifecycle, reviewer-access, evidence quarantine and physical-deletion events. Retention events contain evidence identifiers only, never object keys, storage URLs or evidence bytes.';

COMMIT;
