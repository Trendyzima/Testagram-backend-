#!/usr/bin/env bash
set -euo pipefail

# Existing PostgreSQL volumes do not rerun docker-entrypoint-initdb.d mounts.
# Back up and verify the database before applying the native identity schema.
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
compose_file="${COMPOSE_FILE:-$script_dir/docker-compose.yml}"
env_file="${ENV_FILE:-$script_dir/.env}"
backup_dir="${BACKUP_DIR:-$script_dir/backups}"
mkdir -p "$backup_dir"
chmod 700 "$backup_dir"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
backup="$backup_dir/testagram-pre-native-identity-$stamp.dump"
checksum="$backup.sha256"

docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
  sh -ec 'pg_dump -Fc -U "$POSTGRES_USER" -d "$POSTGRES_DB"' > "$backup"
test -s "$backup"
sha256sum "$backup" > "$checksum"
sha256sum -c "$checksum"
docker run --rm -v "$backup_dir:/backup:ro" postgres:17-alpine \
  pg_restore --list "/backup/$(basename "$backup")" >/dev/null

echo "Verified pre-migration backup: $backup"
echo "SHA-256: $(cut -d ' ' -f1 "$checksum")"
read -r -p "Type APPLY-NATIVE-IDENTITY to apply the additive identity schema: " confirm
if [[ "$confirm" != "APPLY-NATIVE-IDENTITY" ]]; then
  echo "Cancelled. Backup retained; no migrations were applied."
  exit 0
fi

# Recreate only the PostgreSQL container to refresh the read-only migration
# mounts. The named data volume is preserved; this does not delete the DB.
docker compose --env-file "$env_file" -f "$compose_file" up -d postgres
for attempt in $(seq 1 60); do
  if docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
    sh -ec 'pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB"' >/dev/null 2>&1; then
    break
  fi
  if [[ "$attempt" -eq 60 ]]; then
    echo "PostgreSQL did not become ready; no identity migrations were applied." >&2
    exit 1
  fi
  sleep 2
done

for migration in \
  029_admin_security_controls.sql \
  030_identity_registration_pipeline.sql \
  031_identity_registration_hardening.sql \
  032_identity_legacy_intake.sql \
  033_native_identity_verification.sql \
  034_native_identity_integrity.sql \
  035_native_identity_evidence.sql \
  036_native_identity_vendor_neutral.sql; do
  docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
    sh -ec "psql -v ON_ERROR_STOP=1 -U \"\$POSTGRES_USER\" -d \"\$POSTGRES_DB\" -f /docker-entrypoint-initdb.d/$migration"
done

docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
  sh -ec 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atc "
    SELECT to_regclass('\''testagram_identity.verification_sessions'\'') IS NOT NULL
       AND to_regclass('\''testagram_identity.verification_evidence'\'') IS NOT NULL
       AND NOT EXISTS (
         SELECT 1 FROM information_schema.columns
         WHERE table_schema='\''testagram_identity'\''
           AND table_name='\''identity_applications'\''
           AND column_name='\''adult_verification_webhook_received_at'\''
       )
  "' | grep -qx t

echo "Native identity schema applied and verified."
echo "Public registration remains disabled until the backend and Flutter feature flags are deliberately enabled."
