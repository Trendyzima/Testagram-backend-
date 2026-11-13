#!/usr/bin/env bash
# shellcheck disable=SC2016 # Commands are intentionally quoted for expansion inside the PostgreSQL container.
set -euo pipefail
umask 077

# Apply the post-025 self-hosted PostgreSQL migrations to an existing persistent
# volume. PostgreSQL init scripts do not rerun when the volume already exists.
# This is an explicit operator action and creates a validated backup first.
# The social-core migration can deduplicate legacy follow rows; retain the backup.

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
compose_file="$script_dir/docker-compose.yml"
env_file="$script_dir/.env"
backup_dir="${TESTAGRAM_BACKUP_DIR:-$script_dir/backups}"

if [[ "${1:-}" != "--confirm" ]]; then
  echo "Refusing to change the persistent database without explicit confirmation." >&2
  echo "After reviewing this script, run: $0 --confirm" >&2
  exit 2
fi
if [[ ! -f "$env_file" ]]; then
  echo "Missing $env_file; configure the self-hosted environment first." >&2
  exit 2
fi
command -v docker >/dev/null || { echo "docker is required" >&2; exit 2; }
# Prefer the current user's Docker access (including rootless Docker). Fall back
# to sudo only when the daemon is not accessible without elevation.
DOCKER=(docker)
if ! docker info >/dev/null 2>&1; then
  if command -v sudo >/dev/null && sudo docker info >/dev/null 2>&1; then
    DOCKER=(sudo docker)
  else
    echo "Docker is unavailable to the current user, with or without sudo." >&2
    exit 2
  fi
fi
command -v sha256sum >/dev/null || { echo "sha256sum is required" >&2; exit 2; }

mkdir -p "$backup_dir"
chmod 700 "$backup_dir"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
backup="$backup_dir/testagram-pre-current-schema-$stamp.dump"
checksum="$backup.sha256"

postgres_container="$("${DOCKER[@]}" compose --env-file "$env_file" -f "$compose_file" ps -q postgres)"
if [[ -z "$postgres_container" ]]; then
  echo "Starting PostgreSQL for the guarded migration."
  "${DOCKER[@]}" compose --env-file "$env_file" -f "$compose_file" up -d postgres
else
  postgres_state="$("${DOCKER[@]}" inspect --format '{{.State.Status}}' "$postgres_container")"
  if [[ "$postgres_state" != "running" ]]; then
    echo "Starting PostgreSQL from state $postgres_state for the guarded migration."
    "${DOCKER[@]}" compose --env-file "$env_file" -f "$compose_file" up -d postgres
  else
    echo "Reusing the running PostgreSQL container for the guarded migration."
  fi
fi
echo "Waiting for PostgreSQL readiness before creating the backup."
for attempt in $(seq 1 40); do
  if "${DOCKER[@]}" compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
      sh -ec 'pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB"' >/dev/null 2>&1; then
    break
  fi
  if [[ "$attempt" -eq 40 ]]; then
    "${DOCKER[@]}" compose --env-file "$env_file" -f "$compose_file" logs --tail=100 postgres
    echo "PostgreSQL did not become ready; no migration was applied." >&2
    exit 1
  fi
  sleep 2
done

"${DOCKER[@]}" compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
  sh -ec 'pg_dump --format=custom --no-owner --no-acl -U "$POSTGRES_USER" -d "$POSTGRES_DB"' | tee "$backup" >/dev/null
test -s "$backup"
echo "Created pre-migration PostgreSQL backup."
"${DOCKER[@]}" run --rm -v "$backup:/backup.dump:ro" postgres:17-alpine pg_restore --list /backup.dump >/dev/null
sha256sum "$backup" > "$checksum"
chmod 600 "$backup" "$checksum"

echo "Verified pre-migration backup: $backup"
echo "SHA-256: $(cut -d ' ' -f1 "$checksum")"

# Existing persistent volumes do not rerun 000_vps.sql from initdb.
# Bootstrap NOLOGIN privilege groups before migration 035 grants.
"${DOCKER[@]}" compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
  sh -ec 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB"' <<'SQL'
DO $roles$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'testagram_app') THEN
    EXECUTE 'CREATE ROLE testagram_app NOLOGIN';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'service_role') THEN
    EXECUTE 'CREATE ROLE service_role NOLOGIN';
  END IF;
END
$roles$;
SQL

apply_migration() {
  local filename="$1"
  local mounted="/docker-entrypoint-initdb.d/$filename"
  "${DOCKER[@]}" compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
    sh -ec 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -f "$1"' sh "$mounted"
  echo "Applied $filename"
}

# These files are mounted by docker-compose.yml as 026 through 053. Keep this
# order aligned with the dependency order exercised by PostgreSQL CI.
migrations=(
  026_media_replica_ledger.sql
  027_media_replication_outbox.sql
  028_fresh_media_owner_keys.sql
  029_appwrite_identity_link_audit.sql
  030_social_core.sql
  031_federation_actor_consent.sql
  032_communication_core.sql
  033_testagram_polls.sql
  034_wallet_audio_spaces.sql
  035_wallet_mpesa_topups.sql
  036_iptv_social.sql
  037_post_views.sql
  038_threads_browsing_history.sql
  039_thread_interactions.sql
  040_admin_security_controls.sql
  041_identity_registration_pipeline.sql
  042_profile_storage_capacity.sql
  043_social_post_idempotency.sql
  044_federation_remote_follows.sql
  045_hashtag_follows.sql
  046_instabox_avatar_storage.sql
  047_profile_presentation_fields.sql
  048_identity_registration_hardening.sql
  049_identity_idswyft_intake.sql
  050_media_upload_checksum.sql
  051_native_identity_verification.sql
  052_native_identity_integrity.sql
  053_native_identity_evidence.sql
)

for migration in "${migrations[@]}"; do
  apply_migration "$migration"
done

sql="SELECT
  to_regclass('testagram_identity.identity_applications') IS NOT NULL
  AND to_regclass('testagram_identity.verification_sessions') IS NOT NULL
  AND to_regclass('testagram_identity.verification_evidence') IS NOT NULL
  AND to_regclass('testagram_identity.verification_reviews') IS NOT NULL
  AND to_regclass('testagram_auth.sensitive_action_requests') IS NOT NULL
  AND to_regclass('testagram_auth.sensitive_action_approvals') IS NOT NULL
  AND to_regclass('testagram_local.post_create_idempotency') IS NOT NULL
  AND to_regclass('testagram_local.follows') IS NOT NULL
  AND to_regclass('testagram_local.threads') IS NOT NULL
  AND to_regclass('testagram_local.browsing_history') IS NOT NULL
  AND to_regclass('testagram_local.thread_likes') IS NOT NULL
  AND to_regclass('testagram_local.thread_reposts') IS NOT NULL
  AND to_regclass('testagram_local.thread_bookmarks') IS NOT NULL
  AND EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'testagram_app')
  AND EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'service_role');"
result="$("${DOCKER[@]}" compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
  sh -ec 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atc "$1"' sh "$sql")"
if [[ "$result" != "t" ]]; then
  echo "Schema verification failed. Backup retained at $backup." >&2
  exit 1
fi

echo "Verified native identity, two-person approval, social graph and post idempotency schema."
echo "Backup retained: $backup"
