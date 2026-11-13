-- Native verification integrity hardening.
-- Audit events and reviewer decisions are append-only even for privileged application roles.
-- Reviewer identities must resolve to provisioned staff; permission checks remain in the API.
BEGIN;

DO $reviewer_fk$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname='verification_reviews_reviewer_staff_fk'
      AND conrelid='testagram_identity.verification_reviews'::regclass
  ) THEN
    ALTER TABLE testagram_identity.verification_reviews
      ADD CONSTRAINT verification_reviews_reviewer_staff_fk
      FOREIGN KEY (reviewer_appwrite_user_id)
      REFERENCES testagram_auth.staff_roles(appwrite_user_id)
      ON DELETE RESTRICT NOT VALID;
  END IF;
END;
$reviewer_fk$;

DO $decision_semantics$
DECLARE
  constraint_name text;
BEGIN
  -- The initial schema's unnamed CHECK incorrectly required an age-threshold
  -- pass for identity-assurance sessions too. Replace it with purpose-aware rules.
  FOR constraint_name IN
    SELECT conname
    FROM pg_constraint
    WHERE conrelid='testagram_identity.verification_sessions'::regclass
      AND contype='c'
      AND pg_get_constraintdef(oid) LIKE '%verified_age_over_threshold%'
      AND pg_get_constraintdef(oid) LIKE '%status%'
  LOOP
    EXECUTE format(
      'ALTER TABLE testagram_identity.verification_sessions DROP CONSTRAINT %I',
      constraint_name
    );
  END LOOP;

  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname='verification_sessions_decision_semantics_check'
      AND conrelid='testagram_identity.verification_sessions'::regclass
  ) THEN
    ALTER TABLE testagram_identity.verification_sessions
      ADD CONSTRAINT verification_sessions_decision_semantics_check
      CHECK (
        (
          status='verified'
          AND decided_at IS NOT NULL
          AND (
            (purpose='age_assurance' AND verified_age_over_threshold IS TRUE)
            OR (purpose='identity_assurance' AND verified_age_over_threshold IS NULL)
          )
        )
        OR (
          status<>'verified'
          AND (
            verified_age_over_threshold IS NULL
            OR (
              purpose='age_assurance'
              AND verified_age_over_threshold IS FALSE
              AND decided_at IS NOT NULL
            )
          )
        )
      ) NOT VALID;
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname='verification_sessions_decider_staff_fk'
      AND conrelid='testagram_identity.verification_sessions'::regclass
  ) THEN
    ALTER TABLE testagram_identity.verification_sessions
      ADD CONSTRAINT verification_sessions_decider_staff_fk
      FOREIGN KEY (decided_by_appwrite_user_id)
      REFERENCES testagram_auth.staff_roles(appwrite_user_id)
      ON DELETE RESTRICT NOT VALID;
  END IF;
END;
$decision_semantics$;

DO $session_evidence$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname='verification_sessions_submitted_evidence_check'
      AND conrelid='testagram_identity.verification_sessions'::regclass
  ) THEN
    ALTER TABLE testagram_identity.verification_sessions
      ADD CONSTRAINT verification_sessions_submitted_evidence_check
      CHECK (
        status NOT IN ('submitted','manual_review','verified','failed')
        OR (
          evidence_reference IS NOT NULL
          AND length(btrim(evidence_reference)) BETWEEN 8 AND 200
          AND evidence_fingerprint_sha256 IS NOT NULL
          AND octet_length(evidence_fingerprint_sha256)=32
          AND submitted_at IS NOT NULL
        )
      ) NOT VALID;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname='verification_sessions_age_threshold_check'
      AND conrelid='testagram_identity.verification_sessions'::regclass
  ) THEN
    ALTER TABLE testagram_identity.verification_sessions
      ADD CONSTRAINT verification_sessions_age_threshold_check
      CHECK (
        (purpose='age_assurance' AND requested_minimum_age IS NOT NULL AND requested_minimum_age BETWEEN 13 AND 100)
        OR (purpose='identity_assurance' AND requested_minimum_age IS NULL)
      ) NOT VALID;
  END IF;
END;
$session_evidence$;

CREATE INDEX IF NOT EXISTS verification_reviews_reviewer_idx
  ON testagram_identity.verification_reviews(reviewer_appwrite_user_id,created_at DESC);

CREATE OR REPLACE FUNCTION testagram_identity.reject_verification_audit_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $function$
BEGIN
  RAISE EXCEPTION 'verification audit records are append-only';
END;
$function$;

DO $triggers$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_trigger
    WHERE tgname='verification_events_append_only'
      AND tgrelid='testagram_identity.verification_events'::regclass
  ) THEN
    CREATE TRIGGER verification_events_append_only
      BEFORE UPDATE OR DELETE ON testagram_identity.verification_events
      FOR EACH ROW EXECUTE FUNCTION testagram_identity.reject_verification_audit_mutation();
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_trigger
    WHERE tgname='verification_reviews_append_only'
      AND tgrelid='testagram_identity.verification_reviews'::regclass
  ) THEN
    CREATE TRIGGER verification_reviews_append_only
      BEFORE UPDATE OR DELETE ON testagram_identity.verification_reviews
      FOR EACH ROW EXECUTE FUNCTION testagram_identity.reject_verification_audit_mutation();
  END IF;
END;
$triggers$;

REVOKE UPDATE,DELETE ON testagram_identity.verification_events FROM PUBLIC;
REVOKE UPDATE,DELETE ON testagram_identity.verification_reviews FROM PUBLIC;

COMMIT;
