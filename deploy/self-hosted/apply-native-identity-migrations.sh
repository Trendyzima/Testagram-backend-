#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

# Existing PostgreSQL volumes do not rerun docker-entrypoint-initdb.d.
# This operator-gated workflow backs up and verifies the live DB before
# applying the native identity/reviewer migrations in dependency order.
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
compose_file="${COMPOSE_FILE:-$script_dir/docker-compose.yml}"
env_file="${ENV_FILE:-$script_dir/.env}"
backup_dir="${BACKUP_DIR:-$script_dir/backups}"

[[ -f "$compose_file" ]] || { echo "Missing Compose file: $compose_file" >&2; exit 1; }
[[ -f "$env_file" ]] || { echo "Missing environment file: $env_file" >&2; exit 1; }
command -v docker >/dev/null || { echo "docker is required" >&2; exit 1; }
mkdir -p "$backup_dir"
chmod 700 "$backup_dir"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
backup="$backup_dir/testagram-pre-native-identity-$stamp.dump"
checksum="$backup.sha256"

# Take and independently validate a restorable custom-format backup first.
docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
  sh -ec 'pg_dump --format=custom --no-owner --no-acl -U "$POSTGRES_USER" -d "$POSTGRES_DB"' > "$backup"
test -s "$backup"
sha256sum "$backup" > "$checksum"
docker run --rm -v "$backup_dir:/backup:ro" postgres:17-alpine \
  pg_restore --list "/backup/$(basename "$backup")" >/dev/null
sha256sum -c "$checksum"
chmod 600 "$backup" "$checksum"

echo "Verified pre-migration backup: $backup"
echo "SHA-256: $(cut -d ' ' -f1 "$checksum")"
read -r -p "Type APPLY-NATIVE-IDENTITY-MIGRATIONS to continue: " confirm
if [[ "$confirm" != "APPLY-NATIVE-IDENTITY-MIGRATIONS" ]]; then
  echo "Cancelled. Backup retained; no migrations were applied."
  exit 0
fi

# Reconcile the Postgres service with the updated read-only migration mounts.
docker compose --env-file "$env_file" -f "$compose_file" up -d postgres
for file in \
  034_admin_security_controls.sql \
  035_identity_registration_pipeline.sql \
  039_identity_registration_hardening.sql \
  041_native_identity_verification.sql \
  042_native_identity_evidence.sql \
  043_native_identity_integrity.sql \
  044_identity_evidence_access_audit.sql; do
  docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres test -r "/docker-entrypoint-initdb.d/$file"
done

apply_migration() {
  local file="$1"
  echo "Applying $file"
  docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
    sh -ec "psql -v ON_ERROR_STOP=1 -U \"\$POSTGRES_USER\" -d \"\$POSTGRES_DB\" -f '/docker-entrypoint-initdb.d/$file'"
}

apply_migration 034_admin_security_controls.sql
apply_migration 035_identity_registration_pipeline.sql
apply_migration 039_identity_registration_hardening.sql
apply_migration 041_native_identity_verification.sql
apply_migration 042_native_identity_evidence.sql
apply_migration 043_native_identity_integrity.sql
apply_migration 044_identity_evidence_access_audit.sql

docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
  sh -ec 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atc "
    SELECT
      to_regclass('\''testagram_identity.identity_applications'\'') IS NOT NULL
      AND to_regclass('\''testagram_identity.verification_sessions'\'') IS NOT NULL
      AND to_regclass('\''testagram_identity.verification_evidence'\'') IS NOT NULL
      AND to_regclass('\''testagram_identity.verification_events'\'') IS NOT NULL
      AND EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid='\''testagram_identity.verification_events'\''::regclass
          AND conname='\''verification_events_event_type_check'\''
          AND pg_get_constraintdef(oid) LIKE '\''%evidence_access_attempted%'\''
      )
  "' | grep -qx t

echo "Native identity schema and reviewer evidence-access audit verified."
echo "Registration remains disabled. Do not enable it until B2/R2 lifecycle and end-to-end staging acceptance pass."
echo "Retain backup until restore verification and release acceptance are complete."
