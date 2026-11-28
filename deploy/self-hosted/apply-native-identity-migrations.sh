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
    sh -ec 'psql -X -v ON_ERROR_STOP=1 -A -t -U "$POSTGRES_USER" -d "$POSTGRES_DB"' <<'SQL'
WITH checks(check_name, ok) AS (VALUES
  ('admin_rate_limits table', to_regclass('testagram_auth.admin_rate_limits') IS NOT NULL),
  ('admin_permissions table', to_regclass('testagram_auth.admin_permissions') IS NOT NULL),
  ('legacy staff roles archived or absent', to_regclass('testagram_auth.historical_external_staff_roles') IS NOT NULL OR to_regclass('testagram_auth.staff_roles') IS NULL),
  ('legacy staff role audit archived or absent', to_regclass('testagram_auth.historical_external_staff_role_audit') IS NOT NULL OR to_regclass('testagram_auth.staff_role_audit') IS NULL),
  ('historical_external_sensitive_action_requests archived or absent', to_regclass('testagram_auth.historical_external_sensitive_action_requests') IS NOT NULL OR to_regclass('testagram_auth.sensitive_action_requests') IS NULL),
  ('historical_external_sensitive_action_approvals archived or absent', to_regclass('testagram_auth.historical_external_sensitive_action_approvals') IS NOT NULL OR to_regclass('testagram_auth.sensitive_action_approvals') IS NULL),
  ('historical_external_admin_security_audit archived or absent', to_regclass('testagram_auth.historical_external_admin_security_audit') IS NOT NULL OR to_regclass('testagram_auth.admin_security_audit') IS NULL),
  ('historical sensitive-action requests inaccessible to runtime', to_regclass('testagram_auth.historical_external_sensitive_action_requests') IS NULL OR COALESCE((SELECT NOT has_table_privilege(oid,'testagram_auth.historical_external_sensitive_action_requests','SELECT') FROM pg_roles WHERE rolname='testagram_app'),false)),
  ('historical sensitive-action approvals inaccessible to runtime', to_regclass('testagram_auth.historical_external_sensitive_action_approvals') IS NULL OR COALESCE((SELECT NOT has_table_privilege(oid,'testagram_auth.historical_external_sensitive_action_approvals','SELECT') FROM pg_roles WHERE rolname='testagram_app'),false)),
  ('historical admin audit inaccessible to runtime', to_regclass('testagram_auth.historical_external_admin_security_audit') IS NULL OR COALESCE((SELECT NOT has_table_privilege(oid,'testagram_auth.historical_external_admin_security_audit','SELECT') FROM pg_roles WHERE rolname='testagram_app'),false)),
  ('identity_applications table', to_regclass('testagram_identity.identity_applications') IS NOT NULL),
  ('identity_application_reviews table', to_regclass('testagram_identity.identity_application_reviews') IS NOT NULL),
  ('identity_application_reviews.reviewer_user_id', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='identity_application_reviews' AND column_name='reviewer_user_id')),
  ('identity_application_reviews.review_stage', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='identity_application_reviews' AND column_name='review_stage')),
  ('verification_reviews.reviewer_user_id', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='verification_reviews' AND column_name='reviewer_user_id')),
  ('identity_approval_tokens table', to_regclass('testagram_identity.identity_approval_tokens') IS NOT NULL),
  ('historical external provisioning evidence table', to_regclass('testagram_identity.historical_external_registration_provisioning') IS NOT NULL),
  ('verification_sessions table', to_regclass('testagram_identity.verification_sessions') IS NOT NULL),
  ('verification_events table', to_regclass('testagram_identity.verification_events') IS NOT NULL),
  ('verification_reviews table', to_regclass('testagram_identity.verification_reviews') IS NOT NULL),
  ('verification_evidence table', to_regclass('testagram_identity.verification_evidence') IS NOT NULL),
  ('legal_consent_audit table', to_regclass('testagram_identity.legal_consent_audit') IS NOT NULL),
  ('auth sessions table', to_regclass('testagram_auth.sessions') IS NOT NULL),
  ('auth rate_limits table', to_regclass('testagram_auth.rate_limits') IS NOT NULL),
  ('email verification OTP hash column', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_auth' AND table_name='identities' AND column_name='verification_otp_hash')),
  ('email verification OTP expiry column', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_auth' AND table_name='identities' AND column_name='verification_otp_expires_at')),
  ('email verification OTP attempt counter', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_auth' AND table_name='identities' AND column_name='verification_otp_attempts')),
  ('social email notification preferences table', to_regclass('testagram_local.email_notification_preferences') IS NOT NULL),
  ('testagram_app social email preference INSERT', COALESCE((SELECT CASE WHEN to_regclass('testagram_local.email_notification_preferences') IS NOT NULL THEN has_table_privilege(oid,'testagram_local.email_notification_preferences','INSERT') ELSE false END FROM pg_roles WHERE rolname='testagram_app'),false)),
  ('testagram_app social email preference SELECT', COALESCE((SELECT CASE WHEN to_regclass('testagram_local.email_notification_preferences') IS NOT NULL THEN has_table_privilege(oid,'testagram_local.email_notification_preferences','SELECT') ELSE false END FROM pg_roles WHERE rolname='testagram_app'),false)),
  ('testagram_app social email preference UPDATE', COALESCE((SELECT CASE WHEN to_regclass('testagram_local.email_notification_preferences') IS NOT NULL THEN has_table_privilege(oid,'testagram_local.email_notification_preferences','UPDATE') ELSE false END FROM pg_roles WHERE rolname='testagram_app'),false)),
  ('password_reset_tokens table', to_regclass('testagram_auth.password_reset_tokens') IS NOT NULL),
  ('security_audit table', to_regclass('testagram_auth.security_audit') IS NOT NULL),
  ('legacy_account_reconciliation table', to_regclass('testagram_auth.legacy_account_reconciliation') IS NOT NULL),
  ('staff_principals table', to_regclass('testagram_auth.staff_principals') IS NOT NULL),
  ('staff_role_grants table', to_regclass('testagram_auth.staff_role_grants') IS NOT NULL),
  ('staff WebAuthn users table', to_regclass('testagram_auth.staff_webauthn_users') IS NOT NULL),
  ('staff WebAuthn credentials table', to_regclass('testagram_auth.staff_webauthn_credentials') IS NOT NULL),
  ('staff WebAuthn challenges table', to_regclass('testagram_auth.staff_webauthn_challenges') IS NOT NULL),
  ('staff WebAuthn enrollment authorizations table', to_regclass('testagram_auth.staff_webauthn_enrollment_authorizations') IS NOT NULL),
  ('staff WebAuthn dual-control lifecycle events table', to_regclass('testagram_auth.native_staff_lifecycle_events') IS NOT NULL),
  ('staff WebAuthn role permission map', to_regclass('testagram_auth.staff_role_permissions') IS NOT NULL),
  ('staff WebAuthn authorization request binding', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_auth' AND table_name='staff_webauthn_enrollment_authorizations' AND column_name='request_id')),
  ('staff WebAuthn first approver binding', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_auth' AND table_name='staff_webauthn_enrollment_authorizations' AND column_name='first_approver_user_id')),
  ('staff WebAuthn second approver binding', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_auth' AND table_name='staff_webauthn_enrollment_authorizations' AND column_name='second_approver_user_id')),
  ('staff WebAuthn authorization revocation column', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_auth' AND table_name='staff_webauthn_enrollment_authorizations' AND column_name='revoked_at')),
  ('staff WebAuthn authorization validator function', to_regprocedure('testagram_auth.staff_webauthn_enrollment_authorization_is_valid(bigint,uuid,text)') IS NOT NULL),
  ('staff WebAuthn approver permission trigger', EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass('testagram_auth.native_sensitive_action_approvals') AND tgname='native_staff_webauthn_enrollment_approver_permission' AND NOT tgisinternal)),
  ('staff WebAuthn approver permission revalidation function body', COALESCE(pg_get_functiondef(to_regprocedure('testagram_auth.staff_webauthn_enrollment_authorization_is_valid(bigint,uuid,text)')) LIKE '%native_staff_permission_overrides%' AND pg_get_functiondef(to_regprocedure('testagram_auth.staff_webauthn_enrollment_authorization_is_valid(bigint,uuid,text)')) LIKE '%staff_role_permissions%',false)),
  ('testagram_app staff MFA timestamp update', COALESCE((SELECT has_column_privilege(oid,'testagram_auth.staff_principals','mfa_enrolled_at','UPDATE') FROM pg_roles WHERE rolname='testagram_app'),false)),
  ('native_staff_permission_overrides table', to_regclass('testagram_auth.native_staff_permission_overrides') IS NOT NULL),
  ('native_sensitive_action_requests table', to_regclass('testagram_auth.native_sensitive_action_requests') IS NOT NULL),
  ('native_sensitive_action_requests.requested_by_service', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_auth' AND table_name='native_sensitive_action_requests' AND column_name='requested_by_service')),
  ('native sensitive action request source column', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_auth' AND table_name='native_sensitive_action_requests' AND column_name='requested_by_service')),
  ('identity application review stage column', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='identity_application_reviews' AND column_name='review_stage')),
  ('native_sensitive_action_approvals table', to_regclass('testagram_auth.native_sensitive_action_approvals') IS NOT NULL),
  ('native_admin_security_audit table', to_regclass('testagram_auth.native_admin_security_audit') IS NOT NULL),
  ('native sensitive-action request immutability trigger', EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass('testagram_auth.native_sensitive_action_requests') AND tgname='native_sensitive_action_request_immutable' AND NOT tgisinternal)),
  ('native sensitive-action request append-only trigger', EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass('testagram_auth.native_sensitive_action_requests') AND tgname='native_sensitive_action_request_append_only' AND NOT tgisinternal)),
  ('native sensitive-action approver guard trigger', EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass('testagram_auth.native_sensitive_action_approvals') AND tgname='native_sensitive_action_approver_guard' AND NOT tgisinternal)),
  ('native approval WebAuthn RP and credential columns', (SELECT count(*)=2 FROM information_schema.columns WHERE table_schema='testagram_auth' AND table_name='native_sensitive_action_approvals' AND column_name IN ('webauthn_rpid','webauthn_credential_id'))),
  ('native sensitive-action approval append-only trigger', EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass('testagram_auth.native_sensitive_action_approvals') AND tgname='native_sensitive_action_approval_append_only' AND NOT tgisinternal)),
  ('native_registration_provisioning table', to_regclass('testagram_identity.native_registration_provisioning') IS NOT NULL),
  ('native mail return-path token table', to_regclass('testagram_mail.return_path_tokens') IS NOT NULL),
  ('native mail inbound report table', to_regclass('testagram_mail.inbound_reports') IS NOT NULL),
  ('native mail delivery-event transport column', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_mail' AND table_name='delivery_events' AND column_name='transport')),
  ('native mail delivery-event constraint', EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid=to_regclass('testagram_mail.delivery_events') AND conname='testagram_mail_delivery_events_event_type_check' AND pg_get_constraintdef(oid) LIKE '%dsn.bounced%')),
  ('testagram_app mail delivery-event DELETE', COALESCE((SELECT CASE WHEN to_regclass('testagram_mail.delivery_events') IS NOT NULL THEN has_table_privilege(oid,'testagram_mail.delivery_events','DELETE') ELSE false END FROM pg_roles WHERE rolname='testagram_app'),false)),
  ('testagram_app social unsubscribe-token DELETE', COALESCE((SELECT CASE WHEN to_regclass('testagram_mail.social_unsubscribe_tokens') IS NOT NULL THEN has_table_privilege(oid,'testagram_mail.social_unsubscribe_tokens','DELETE') ELSE false END FROM pg_roles WHERE rolname='testagram_app'),false)),
  ('testagram_app native return-path SELECT', COALESCE((SELECT CASE WHEN to_regclass('testagram_mail.return_path_tokens') IS NOT NULL THEN has_table_privilege(oid,'testagram_mail.return_path_tokens','SELECT') ELSE false END FROM pg_roles WHERE rolname='testagram_app'),false)),
  ('testagram_app native return-path INSERT', COALESCE((SELECT CASE WHEN to_regclass('testagram_mail.return_path_tokens') IS NOT NULL THEN has_table_privilege(oid,'testagram_mail.return_path_tokens','INSERT') ELSE false END FROM pg_roles WHERE rolname='testagram_app'),false)),
  ('testagram_app native return-path UPDATE', COALESCE((SELECT CASE WHEN to_regclass('testagram_mail.return_path_tokens') IS NOT NULL THEN has_table_privilege(oid,'testagram_mail.return_path_tokens','UPDATE') ELSE false END FROM pg_roles WHERE rolname='testagram_app'),false)),
  ('testagram_app inbound report SELECT', COALESCE((SELECT CASE WHEN to_regclass('testagram_mail.inbound_reports') IS NOT NULL THEN has_table_privilege(oid,'testagram_mail.inbound_reports','SELECT') ELSE false END FROM pg_roles WHERE rolname='testagram_app'),false)),
  ('testagram_app inbound report INSERT', COALESCE((SELECT CASE WHEN to_regclass('testagram_mail.inbound_reports') IS NOT NULL THEN has_table_privilege(oid,'testagram_mail.inbound_reports','INSERT') ELSE false END FROM pg_roles WHERE rolname='testagram_app'),false)),
  ('testagram_app inbound report DELETE', COALESCE((SELECT CASE WHEN to_regclass('testagram_mail.inbound_reports') IS NOT NULL THEN has_table_privilege(oid,'testagram_mail.inbound_reports','DELETE') ELSE false END FROM pg_roles WHERE rolname='testagram_app'),false)),
  ('testagram_app inbound report sequence USAGE', COALESCE((SELECT CASE WHEN to_regclass('testagram_mail.inbound_reports') IS NOT NULL THEN has_sequence_privilege(oid,pg_get_serial_sequence('testagram_mail.inbound_reports','id'),'USAGE') ELSE false END FROM pg_roles WHERE rolname='testagram_app'),false)),

  ('testagram_mail.outbox table', to_regclass('testagram_mail.outbox') IS NOT NULL),
  ('mail outbox encrypted payload and lifecycle columns', (SELECT count(*)=10 FROM information_schema.columns WHERE table_schema='testagram_mail' AND table_name='outbox' AND column_name IN ('payload_ciphertext','payload_nonce','status','attempts','available_at','lease_until','sent_at','provider_message_id','last_error_code','updated_at'))),
  ('mail outbox social consent recipient column', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_mail' AND table_name='outbox' AND column_name='preference_user_id')),
  ('mail outbox social consent category column', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_mail' AND table_name='outbox' AND column_name='preference_category')),
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
  ('native sensitive action approver_user_id', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_auth' AND table_name='native_sensitive_action_approvals' AND column_name='approver_user_id')),
  ('native admin audit actor_user_id', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_auth' AND table_name='native_admin_security_audit' AND column_name='actor_user_id')),
  ('identity approval first approver', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='identity_approval_tokens' AND column_name='first_approver_user_id')),
  ('identity approval second approver', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='identity_approval_tokens' AND column_name='second_approver_user_id')),
  ('verification session native decider', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='verification_sessions' AND column_name='decided_by_user_id')),
  ('identity application native approval request', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_identity' AND table_name='identity_applications' AND column_name='native_sensitive_action_request_id')),
  ('native auth password reset required flag', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_auth' AND table_name='identities' AND column_name='password_reset_required')),
  ('two-person identity approval trigger', EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass('testagram_identity.identity_approval_tokens') AND tgname='identity_approval_token_requires_two_person_approval' AND NOT tgisinternal)),
  ('verification events append-only trigger', EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass('testagram_identity.verification_events') AND tgname='verification_events_append_only' AND NOT tgisinternal)),
  ('verification reviews append-only trigger', EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass('testagram_identity.verification_reviews') AND tgname='verification_reviews_append_only' AND NOT tgisinternal)),
  ('legal consent audit append-only trigger', EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass('testagram_identity.legal_consent_audit') AND tgname='legal_consent_audit_append_only' AND NOT tgisinternal)),
  ('verification event audit constraint', EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid=to_regclass('testagram_identity.verification_events') AND conname='verification_events_event_type_check' AND pg_get_constraintdef(oid) LIKE '%evidence_access_attempted%' AND pg_get_constraintdef(oid) LIKE '%identity_evidence_quarantined%' AND pg_get_constraintdef(oid) LIKE '%identity_evidence_deleted%')),
  ('Federation actor consent column', EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='testagram_local' AND table_name='accounts' AND column_name='federation_enabled'))
)
SELECT check_name FROM checks WHERE NOT COALESCE(ok,false) ORDER BY check_name;
SQL
}

schema_ready() {
  # Reuse the diagnostic predicates as the single source of truth. Keeping a
  # second hand-escaped SQL copy caused quoting drift and a production syntax
  # error before migration execution.
  local failed_checks
  failed_checks="$(schema_diagnostics)"
  if [[ -z "$failed_checks" ]]; then
    printf 't\n'
  else
    printf 'f\n'
  fi
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

# The migration SQL files are bind-mounted individually. A git reset can replace their host inodes while
# the long-lived PostgreSQL container still sees the old mounted inode. Recreate only the container
# (the named data volume is preserved) after the verified backup/restore, so migrations execute
# the exact files from the checked-out release rather than stale SQL from a previous deployment.
docker compose --env-file "$env_file" -f "$compose_file" up -d --force-recreate postgres
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
  049_native_auth_cutover_foundation.sql \
  050_native_approved_identity_provisioning.sql \
  051_native_auth_runtime_grants.sql \
  053_native_staff_reviewer_grants.sql \
  054_native_admin_console.sql \
  055_native_identity_approval_ledger.sql \
  056_native_verification_decider.sql \
  057_testagram_mail_outbox.sql \
  058_testagram_mail_outbox_runtime_role.sql \
  058a_native_identity_historical_account_archive.sql \
  058b_native_identity_historical_staff_archive.sql \
  058c_native_identity_historical_admin_archive.sql \
  058d_native_identity_historical_attribution_archive.sql \
  059_native_identity_approval_circuit.sql \
  20261121_native_identity_intake_optional_fingerprint.sql \
  060_native_staff_webauthn.sql \
  061_native_sensitive_action_integrity.sql \
  20261127_native_staff_lifecycle_executor.sql \
  20261128_native_staff_webauthn_dual_control.sql \
  20261124_testagram_email_verification_otp.sql \
  20261124_testagram_social_email_consent.sql \
  20261125_testagram_mail_delivery_events.sql \
  20261126_testagram_native_mail_feedback.sql \
  20261127_identity_evidence_retention_audit.sql; do
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
apply_migration 049_native_auth_cutover_foundation.sql
apply_migration 050_native_approved_identity_provisioning.sql
apply_migration 051_native_auth_runtime_grants.sql
apply_migration 053_native_staff_reviewer_grants.sql
apply_migration 054_native_admin_console.sql
apply_migration 055_native_identity_approval_ledger.sql
apply_migration 056_native_verification_decider.sql
apply_migration 057_testagram_mail_outbox.sql
apply_migration 058_testagram_mail_outbox_runtime_role.sql
apply_migration 058a_native_identity_historical_account_archive.sql
apply_migration 058b_native_identity_historical_staff_archive.sql
apply_migration 058c_native_identity_historical_admin_archive.sql
apply_migration 058d_native_identity_historical_attribution_archive.sql
apply_migration 059_native_identity_approval_circuit.sql
apply_migration 20261121_native_identity_intake_optional_fingerprint.sql
apply_migration 060_native_staff_webauthn.sql
apply_migration 061_native_sensitive_action_integrity.sql
apply_migration 20261127_native_staff_lifecycle_executor.sql
apply_migration 20261128_native_staff_webauthn_dual_control.sql
apply_migration 20261128_native_staff_webauthn_approver_permission_revalidation.sql
apply_migration 20261124_testagram_email_verification_otp.sql
apply_migration 20261124_testagram_social_email_consent.sql
apply_migration 20261125_testagram_mail_delivery_events.sql
apply_migration 20261126_testagram_native_mail_feedback.sql
apply_migration 20261127_identity_evidence_retention_audit.sql

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

echo "Verified native identity applications, independent approvals, one-time provisioning, native session/refresh storage, password recovery, encrypted Testagram-native mail outbox, VERP return paths, inbound feedback ledger, Federation actor-consent eligibility, runtime grants, native staff reviewer grants, native admin-console ledgers, native staff lifecycle invitation and audit ledgers, native identity approval circuit, staged evidence/account reviews, UUID-based reviewer columns and verification decision attribution, evidence audit, status access, identity evidence retention quarantine and physical deletion audit, and native approval circuit."
echo "Retain the verified pre-migration backup until release acceptance and a separate restore drill are complete."
