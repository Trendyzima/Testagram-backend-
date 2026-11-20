#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

# Existing PostgreSQL volumes do not rerun docker-entrypoint-initdb.d.
# This helper exits when the complete schema is present and requires explicit
# authorization plus a verified backup before applying the migration chain.
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
compose_file="${COMPOSE_FILE:-$script_dir/docker-compose.yml}"
env_file="${ENV_FILE:-$script_dir/.env}"
backup_dir="${BACKUP_DIR:-$script_dir/backups}"

[[ -f "$compose_file" ]] || { echo "Missing Compose file: $compose_file" >&2; exit 1; }
[[ -f "$env_file" ]] || { echo "Missing environment file: $env_file" >&2; exit 1; }
command -v docker >/dev/null || { echo "docker is required" >&2; exit 1; }
docker compose --env-file "$env_file" -f "$compose_file" config -q
docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
  sh -ec 'pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB"'

schema_diagnostics() {
  docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
    sh -ec 'psql -X -A -t -U "$POSTGRES_USER" -d "$POSTGRES_DB"' <<'SQL'
WITH checks(check_name, ok) AS (VALUES
  ('admin_rate_limits table', to_regclass('testagram_auth.admin_rate_limits') IS NOT NULL),
  ('admin_permissions table', to_regclass('testagram_auth.admin_permissions') IS NOT NULL),
  ('sensitive_action_requests table', to_regclass('testagram_auth.sensitive_action_requests') IS NOT NULL),
  ('sensitive_action_approvals table', to_regclass('testagram_auth.sensitive_action_approvals') IS NOT NULL),
  ('admin_security_audit table', to_regclass('testagram_auth.admin_security_audit') IS NOT NULL),
  ('identity_applications table', to_regclass('testagram_identity.identity_applications') IS NOT NULL),
  ('identity_application_reviews table', to_regclass('testagram_identity.identity_application_reviews') IS NOT NULL),
  ('identity_application_reviews.reviewer_user_id', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='identity_application_reviews' AND column_name='reviewer_user_id')),
  ('verification_reviews.reviewer_user_id', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='verification_reviews' AND column_name='reviewer_user_id')),
  ('identity_approval_tokens table', to_regclass('testagram_identity.identity_approval_tokens') IS NOT NULL),
  ('registration_provisioning table', to_regclass('testagram_identity.registration_provisioning') IS NOT NULL),
  ('verification_sessions table', to_regclass('testagram_identity.verification_sessions') IS NOT NULL),
  ('verification_events table', to_regclass('testagram_identity.verification_events') IS NOT NULL),
  ('verification_reviews table', to_regclass('testagram_identity.verification_reviews') IS NOT NULL),
  ('verification_evidence table', to_regclass('testagram_identity.verification_evidence') IS NOT NULL),
  ('legal_consent_audit table', to_regclass('testagram_identity.legal_consent_audit') IS NOT NULL),
  ('auth sessions table', to_regclass('testagram_auth.sessions') IS NOT NULL),
  ('auth rate_limits table', to_regclass('testagram_auth.rate_limits') IS NOT NULL),
  ('password_reset_tokens table', to_regclass('testagram_auth.password_reset_tokens') IS NOT NULL),
  ('security_audit table', to_regclass('testagram_auth.security_audit') IS NOT NULL),
  ('legacy_account_reconciliation table', to_regclass('testagram_auth.legacy_account_reconciliation') IS NOT NULL),
  ('staff_principals table', to_regclass('testagram_auth.staff_principals') IS NOT NULL),
  ('staff_role_grants table', to_regclass('testagram_auth.staff_role_grants') IS NOT NULL),
  ('native_staff_permission_overrides table', to_regclass('testagram_auth.native_staff_permission_overrides') IS NOT NULL),
  ('native_sensitive_action_requests table', to_regclass('testagram_auth.native_sensitive_action_requests') IS NOT NULL),
  ('native sensitive action request source column', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_auth' AND table_name='native_sensitive_action_requests' AND column_name='requested_by_service')),
  ('identity application review stage column', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='identity_application_reviews' AND column_name='review_stage')),
  ('native_sensitive_action_approvals table', to_regclass('testagram_auth.native_sensitive_action_approvals') IS NOT NULL),
  ('native_admin_security_audit table', to_regclass('testagram_auth.native_admin_security_audit') IS NOT NULL),
  ('native_registration_provisioning table', to_regclass('testagram_identity.native_registration_provisioning') IS NOT NULL),
  ('testagram_mail.outbox table', to_regclass('testagram_mail.outbox') IS NOT NULL),
  ('mail outbox encrypted payload and lifecycle columns', (SELECT count(*)=10 FROM information_schema.columns WHERE table_schema='testagram_mail' AND table_name='outbox' AND column_name IN ('payload_ciphertext','payload_nonce','status','attempts','available_at','lease_until','sent_at','provider_message_id','last_error_code','updated_at'))),
  ('testagram_app mail schema USAGE', COALESCE((SELECT has_schema_privilege(oid,'testagram_mail','USAGE') FROM pg_roles WHERE rolname='testagram_app'),false)),
  ('testagram_app mail outbox SELECT', COALESCE((SELECT has_table_privilege(oid,'testagram_mail.outbox','SELECT') FROM pg_roles WHERE rolname='testagram_app'),false)),
  ('testagram_app mail outbox INSERT', COALESCE((SELECT has_table_privilege(oid,'testagram_mail.outbox','INSERT') FROM pg_roles WHERE rolname='testagram_app'),false)),
  ('testagram_app mail outbox UPDATE', COALESCE((SELECT has_table_privilege(oid,'testagram_mail.outbox','UPDATE') FROM pg_roles WHERE rolname='testagram_app'),false)),
  ('testagram_app mail sequence USAGE', COALESCE((SELECT has_sequence_privilege(oid,'testagram_mail.outbox_id_seq','USAGE') FROM pg_roles WHERE rolname='testagram_app'),false)),
  ('testagram_app mail sequence SELECT', COALESCE((SELECT has_sequence_privilege(oid,'testagram_mail.outbox_id_seq','SELECT') FROM pg_roles WHERE rolname='testagram_app'),false)),
  ('identity application status access token digest', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='identity_applications' AND column_name='status_access_token_sha256')),
  ('identity application evidence fingerprint nullable', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='identity_applications' AND column_name='evidence_fingerprint_sha256' AND is_nullable='YES')),
  ('identity evidence-complete approval trigger', EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass('testagram_identity.identity_approval_tokens') AND tgname='identity_approval_token_requires_verified_evidence' AND NOT tgisinternal)),
  ('identity approval consumed native user column', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='identity_approval_tokens' AND column_name='consumed_by_native_user_id')),
  ('sensitive action approver_user_id', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_auth' AND table_name='sensitive_action_approvals' AND column_name='approver_user_id')),
  ('admin audit actor_user_id', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_auth' AND table_name='admin_security_audit' AND column_name='actor_user_id')),
  ('identity approval first approver', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='identity_approval_tokens' AND column_name='first_approver_user_id')),
  ('identity approval second approver', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='identity_approval_tokens' AND column_name='second_approver_user_id')),
  ('verification session native decider', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='verification_sessions' AND column_name='decided_by_user_id')),
  ('identity application native approval request', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='identity_applications' AND column_name='native_sensitive_action_request_id')),
  ('native auth password reset required flag', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_auth' AND table_name='identities' AND column_name='password_reset_required')),
  ('two-person identity approval trigger', EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass('testagram_identity.identity_approval_tokens') AND tgname='identity_approval_token_requires_two_person_approval' AND NOT tgisinternal)),
  ('verification events append-only trigger', EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass('testagram_identity.verification_events') AND tgname='verification_events_append_only' AND NOT tgisinternal)),
  ('verification reviews append-only trigger', EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass('testagram_identity.verification_reviews') AND tgname='verification_reviews_append_only' AND NOT tgisinternal)),
  ('legal consent audit append-only trigger', EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass('testagram_identity.legal_consent_audit') AND tgname='legal_consent_audit_append_only' AND NOT tgisinternal)),
  ('verification event audit constraint', EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid=to_regclass('testagram_identity.verification_events') AND conname='verification_events_event_type_check' AND pg_get_constraintdef(oid) LIKE '%evidence_access_attempted%')),
  ('Federation actor consent column', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_local' AND table_name='accounts' AND column_name='federation_enabled'))
)
SELECT check_name FROM checks WHERE NOT COALESCE(ok,false) ORDER BY check_name;
SQL
}

schema_ready() {
  docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
    sh -ec 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atc "
      SELECT
        to_regclass('\''testagram_auth.admin_rate_limits'\'') IS NOT NULL
        AND to_regclass('\''testagram_auth.admin_permissions'\'') IS NOT NULL
        AND to_regclass('\''testagram_auth.sensitive_action_requests'\'') IS NOT NULL
        AND to_regclass('\''testagram_auth.sensitive_action_approvals'\'') IS NOT NULL
        AND to_regclass('\''testagram_auth.admin_security_audit'\'') IS NOT NULL
        AND to_regclass('\''testagram_identity.identity_applications'\'') IS NOT NULL
        AND to_regclass('\''testagram_identity.identity_application_reviews'\'') IS NOT NULL
        AND EXISTS (
          SELECT 1 FROM information_schema.columns
          WHERE table_schema='\''testagram_identity'\''
            AND table_name='\''identity_application_reviews'\''
            AND column_name='\''reviewer_user_id'\''
        )
        AND EXISTS (
          SELECT 1 FROM information_schema.columns
          WHERE table_schema='\''testagram_identity'\''
            AND table_name='\''verification_reviews'\''
            AND column_name='\''reviewer_user_id'\''
        )
        AND to_regclass('\''testagram_identity.identity_approval_tokens'\'') IS NOT NULL
        AND to_regclass('\''testagram_identity.registration_provisioning'\'') IS NOT NULL
        AND to_regclass('\''testagram_identity.verification_sessions'\'') IS NOT NULL
        AND to_regclass('\''testagram_identity.verification_events'\'') IS NOT NULL
        AND to_regclass('\''testagram_identity.verification_reviews'\'') IS NOT NULL
        AND to_regclass('\''testagram_identity.verification_evidence'\'') IS NOT NULL
        AND to_regclass('\''testagram_identity.legal_consent_audit'\'') IS NOT NULL
        AND to_regclass('\''testagram_auth.sessions'\'') IS NOT NULL
        AND to_regclass('\''testagram_auth.rate_limits'\'') IS NOT NULL
        AND to_regclass('\''testagram_auth.password_reset_tokens'\'') IS NOT NULL
        AND to_regclass('\''testagram_auth.security_audit'\'') IS NOT NULL
        AND to_regclass('\''testagram_auth.legacy_account_reconciliation'\'') IS NOT NULL
        AND to_regclass('\''testagram_auth.staff_principals'\'') IS NOT NULL
        AND to_regclass('\''testagram_auth.staff_role_grants'\'') IS NOT NULL
        AND to_regclass('\''testagram_auth.native_staff_permission_overrides'\'') IS NOT NULL
        AND to_regclass('\''testagram_auth.native_sensitive_action_requests'\'') IS NOT NULL
        AND EXISTS (
          SELECT 1 FROM information_schema.columns
          WHERE table_schema='\''testagram_auth'\'' AND table_name='\''native_sensitive_action_requests'\''
            AND column_name='\''requested_by_service'\''
        )
        AND EXISTS (
          SELECT 1 FROM information_schema.columns
          WHERE table_schema='\''testagram_identity'\'' AND table_name='\''identity_application_reviews'\''
            AND column_name='\''review_stage'\''
        )
        AND to_regclass('\''testagram_auth.native_sensitive_action_approvals'\'') IS NOT NULL
        AND to_regclass('\''testagram_auth.native_admin_security_audit'\'') IS NOT NULL
        AND to_regclass('\''testagram_identity.native_registration_provisioning'\'') IS NOT NULL
        AND to_regclass('\''testagram_mail.outbox'\'') IS NOT NULL
        AND (
          SELECT count(*) = 10
          FROM information_schema.columns
          WHERE table_schema='\''testagram_mail'\''
            AND table_name='\''outbox'\''
            AND column_name IN (
              '\''payload_ciphertext'\'', '\''payload_nonce'\'', '\''status'\'',
              '\''attempts'\'', '\''available_at'\'', '\''lease_until'\'',
              '\''sent_at'\'', '\''provider_message_id'\'', '\''last_error_code'\'',
              '\''updated_at'\''
            )
        )
        AND COALESCE((SELECT has_schema_privilege(oid,'\''testagram_mail'\'','\''USAGE'\'') FROM pg_roles WHERE rolname='\''testagram_app'\''), false)
        AND COALESCE((SELECT has_table_privilege(oid,'\''testagram_mail.outbox'\'','\''SELECT'\'') FROM pg_roles WHERE rolname='\''testagram_app'\''), false)
        AND COALESCE((SELECT has_table_privilege(oid,'\''testagram_mail.outbox'\'','\''INSERT'\'') FROM pg_roles WHERE rolname='\''testagram_app'\''), false)
        AND COALESCE((SELECT has_table_privilege(oid,'\''testagram_mail.outbox'\'','\''UPDATE'\'') FROM pg_roles WHERE rolname='\''testagram_app'\''), false)
        AND COALESCE((SELECT has_sequence_privilege(oid,'\''testagram_mail.outbox_id_seq'\'','\''USAGE'\'') FROM pg_roles WHERE rolname='\''testagram_app'\''), false)
        AND COALESCE((SELECT has_sequence_privilege(oid,'\''testagram_mail.outbox_id_seq'\'','\''SELECT'\'') FROM pg_roles WHERE rolname='\''testagram_app'\''), false)
        AND EXISTS (
          SELECT 1 FROM information_schema.columns
          WHERE table_schema='\''testagram_identity'\''
            AND table_name='\''identity_applications'\''
            AND column_name='\''status_access_token_sha256'\''
        )
        AND EXISTS (
          SELECT 1 FROM information_schema.columns
          WHERE table_schema='\''testagram_identity'\''
            AND table_name='\''identity_applications'\''
            AND column_name='\''evidence_fingerprint_sha256'\''
            AND is_nullable='\''YES'\''
        )
        AND EXISTS (
          SELECT 1 FROM pg_trigger
          WHERE tgrelid=to_regclass('\''testagram_identity.identity_approval_tokens'\'')
            AND tgname='\''identity_approval_token_requires_verified_evidence'\''
            AND NOT tgisinternal
        )
        AND EXISTS (
          SELECT 1 FROM information_schema.columns
          WHERE table_schema='\''testagram_identity'\''
            AND table_name='\''identity_approval_tokens'\''
            AND column_name='\''consumed_by_native_user_id'\''
        )
        AND EXISTS (
          SELECT 1 FROM information_schema.columns
          WHERE table_schema='\''testagram_auth'\''
            AND table_name='\''sensitive_action_approvals'\''
            AND column_name='\''approver_user_id'\''
        )
        AND EXISTS (
          SELECT 1 FROM information_schema.columns
          WHERE table_schema='\''testagram_auth'\''
            AND table_name='\''admin_security_audit'\''
            AND column_name='\''actor_user_id'\''
        )
        AND EXISTS (
          SELECT 1 FROM information_schema.columns
          WHERE table_schema='\''testagram_identity'\''
            AND table_name='\''identity_approval_tokens'\''
            AND column_name='\''first_approver_user_id'\''
        )
        AND EXISTS (
          SELECT 1 FROM information_schema.columns
          WHERE table_schema='\''testagram_identity'\''
            AND table_name='\''identity_approval_tokens'\''
            AND column_name='\''second_approver_user_id'\''
        )
        AND EXISTS (
          SELECT 1 FROM information_schema.columns
          WHERE table_schema='\''testagram_identity'\''
            AND table_name='\''verification_sessions'\''
            AND column_name='\''decided_by_user_id'\''
        )
        AND EXISTS (
          SELECT 1 FROM information_schema.columns
          WHERE table_schema='\''testagram_identity'\''
            AND table_name='\''identity_applications'\''
            AND column_name='\''native_sensitive_action_request_id'\''
        )
        AND EXISTS (
          SELECT 1 FROM information_schema.columns
          WHERE table_schema='\''testagram_auth'\''
            AND table_name='\''identities'\''
            AND column_name='\''password_reset_required'\''
        )
        AND EXISTS (
          SELECT 1 FROM pg_trigger
          WHERE tgrelid=to_regclass('\''testagram_identity.identity_approval_tokens'\'')
            AND tgname='\''identity_approval_token_requires_two_person_approval'\''
            AND NOT tgisinternal
        )
        AND EXISTS (
          SELECT 1 FROM pg_trigger
          WHERE tgrelid=to_regclass('\''testagram_identity.verification_events'\'')
            AND tgname='\''verification_events_append_only'\''
            AND NOT tgisinternal
        )
        AND EXISTS (
          SELECT 1 FROM pg_trigger
          WHERE tgrelid=to_regclass('\''testagram_identity.verification_reviews'\'')
            AND tgname='\''verification_reviews_append_only'\''
            AND NOT tgisinternal
        )
        AND EXISTS (
          SELECT 1 FROM pg_trigger
          WHERE tgrelid=to_regclass('\''testagram_identity.legal_consent_audit'\'')
            AND tgname='\''legal_consent_audit_append_only'\''
            AND NOT tgisinternal
        )
        AND EXISTS (
          SELECT 1 FROM pg_constraint
          WHERE conrelid=to_regclass('\''testagram_identity.verification_events'\'')
            AND conname='\''verification_events_event_type_check'\''
            AND pg_get_constraintdef(oid) LIKE '\''%evidence_access_attempted%'\''
        )
        AND EXISTS (
          SELECT 1 FROM information_schema.columns
          WHERE table_schema='\''testagram_local'\''
            AND table_name='\''accounts'\''
            AND column_name='\''federation_enabled'\''
        )
    "'
}

if [[ "$(schema_ready)" == "t" ]]; then
  echo "Native identity and Federation actor-consent PostgreSQL schemas are complete; migration preflight passed without changing data."
  exit 0
fi

if [[ "${CONFIRM_NATIVE_IDENTITY_MIGRATIONS:-}" != "APPLY-NATIVE-IDENTITY-MIGRATIONS" ]]; then
  echo "::error::Native identity schema is incomplete. No migrations were applied. Re-run the production workflow with confirm_native_identity_migrations=true after reviewing the backup and migration plan."
  exit 1
fi

mkdir -p "$backup_dir"
chmod 700 "$backup_dir"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
backup="$backup_dir/testagram-pre-native-identity-$stamp.dump"
checksum="$backup.sha256"
verify_db="testagram_backup_verify_${stamp//[^A-Za-z0-9]/_}"
container_backup="/tmp/testagram-pre-native-identity-$stamp.dump"

docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
  sh -ec 'pg_dump --format=custom --no-owner --no-acl -U "$POSTGRES_USER" -d "$POSTGRES_DB"' > "$backup"
test -s "$backup"
sha256sum "$backup" > "$checksum"
docker run --rm -v "$backup_dir:/backup:ro" postgres:17-alpine \
  pg_restore --list "/backup/$(basename "$backup")" >/dev/null
sha256sum -c "$checksum"
chmod 600 "$backup" "$checksum"

# Prove the archive can restore into a fresh database on the same PostgreSQL
# server; pg_restore --list alone only proves the archive table of contents.
cleanup_restore_test() {
  docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
    sh -ec 'dropdb --if-exists -U "$POSTGRES_USER" "$1" || true; rm -f "$2"' sh "$verify_db" "$container_backup" >/dev/null 2>&1 || true
}
docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
  sh -ec 'createdb -U "$POSTGRES_USER" "$1"' sh "$verify_db"
trap cleanup_restore_test EXIT
cat "$backup" | docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
  sh -ec 'cat > "$1"' sh "$container_backup"
docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
  sh -ec 'pg_restore --exit-on-error --no-owner --no-acl -U "$POSTGRES_USER" -d "$1" "$2"' sh "$verify_db" "$container_backup"
cleanup_restore_test
trap - EXIT
echo "Verified SHA-256 and completed a full restore test: $backup"

docker compose --env-file "$env_file" -f "$compose_file" up -d postgres
for file in \
  029_federation_actor_consent.sql \
  034_admin_security_controls.sql \
  035_identity_registration_pipeline.sql \
  039_identity_registration_hardening.sql \
  041_native_identity_verification.sql \
  042_native_identity_evidence.sql \
  043_native_identity_integrity.sql \
  044_identity_evidence_access_audit.sql \
  045_native_identity_status_access.sql \
  047_appwrite_registration_consent.sql \
  049_native_auth_cutover_foundation.sql \
  050_native_approved_identity_provisioning.sql \
  051_native_auth_runtime_grants.sql \
  053_native_staff_reviewer_grants.sql \
  054_native_admin_console.sql \
  055_native_identity_approval_ledger.sql \
  056_native_verification_decider.sql \
  057_testagram_mail_outbox.sql \
  058_testagram_mail_outbox_runtime_role.sql \
  059_native_identity_approval_circuit.sql \
  20261121_native_identity_intake_optional_fingerprint.sql; do
  docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres test -r "/docker-entrypoint-initdb.d/$file"
done

apply_migration() {
  local file="$1"
  echo "Applying $file"
  docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
    sh -ec "psql -v ON_ERROR_STOP=1 -U \"\$POSTGRES_USER\" -d \"\$POSTGRES_DB\" -f '/docker-entrypoint-initdb.d/$file'"
}

apply_migration 029_federation_actor_consent.sql
apply_migration 034_admin_security_controls.sql
apply_migration 035_identity_registration_pipeline.sql
apply_migration 039_identity_registration_hardening.sql
apply_migration 041_native_identity_verification.sql
apply_migration 042_native_identity_evidence.sql
apply_migration 043_native_identity_integrity.sql
apply_migration 044_identity_evidence_access_audit.sql
apply_migration 045_native_identity_status_access.sql
apply_migration 047_appwrite_registration_consent.sql
apply_migration 049_native_auth_cutover_foundation.sql
apply_migration 050_native_approved_identity_provisioning.sql
apply_migration 051_native_auth_runtime_grants.sql
apply_migration 053_native_staff_reviewer_grants.sql
apply_migration 054_native_admin_console.sql
apply_migration 055_native_identity_approval_ledger.sql
apply_migration 056_native_verification_decider.sql
apply_migration 057_testagram_mail_outbox.sql
apply_migration 058_testagram_mail_outbox_runtime_role.sql
apply_migration 059_native_identity_approval_circuit.sql
apply_migration 20261121_native_identity_intake_optional_fingerprint.sql

if [[ "$(schema_ready)" != "t" ]]; then
  echo "::error::Native identity schema verification failed after migration. Backend startup must remain blocked."
  failed_checks="$(schema_diagnostics)"
  if [[ -n "$failed_checks" ]]; then
    echo "Failed schema acceptance predicates:"
    echo "$failed_checks"
  else
    echo "Diagnostic query returned no failed individual predicates; inspect PostgreSQL logs and schema_ready SQL."
  fi
  exit 1
fi

echo "Verified native identity applications, independent approvals, one-time provisioning, native session/refresh storage, password recovery, encrypted Cloudflare mail outbox, Federation actor-consent eligibility, runtime grants, native staff reviewer grants, native admin-console ledgers, native identity approval circuit, staged evidence/account reviews, UUID-based reviewer columns and verification decision attribution, evidence audit, status access, and native approval circuit."
echo "Retain the verified pre-migration backup until release acceptance and a separate restore drill are complete."
