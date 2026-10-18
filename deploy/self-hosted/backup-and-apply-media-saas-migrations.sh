#!/usr/bin/env bash
set -euo pipefail
umask 077

# Safe operator workflow for an existing self-hosted VPS. This script refuses
# to apply either migration unless it first creates and validates a database
# backup outside the container.
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
compose_file="$script_dir/docker-compose.yml"
env_file="$script_dir/.env"
backup_dir="${TESTAGRAM_BACKUP_DIR:-$script_dir/backups}"

if [[ ! -f "$env_file" ]]; then
  echo "Missing $env_file; copy .env.example to .env and configure it first." >&2
  exit 1
fi
command -v docker >/dev/null || { echo "docker is required" >&2; exit 1; }
command -v pg_restore >/dev/null || { echo "pg_restore is required to validate the backup archive" >&2; exit 1; }

mkdir -p "$backup_dir"
chmod 700 "$backup_dir"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
backup="$backup_dir/testagram-pre-migrations-$stamp.dump"
checksum="$backup.sha256"

docker compose --env-file "$env_file" -f "$compose_file" up -d postgres
# pg_dump custom format is compressed and suitable for a tested restore.
docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
  sh -ec 'pg_dump --format=custom --no-owner --no-acl -U "$POSTGRES_USER" -d "$POSTGRES_DB"' > "$backup"
test -s "$backup"
pg_restore --list "$backup" >/dev/null
sha256sum "$backup" > "$checksum"
chmod 600 "$backup" "$checksum"

echo "Verified backup archive: $backup"
echo "SHA-256: $(cut -d ' ' -f1 "$checksum")"
echo "Next: migrations 008 and 009 will be applied to the configured database."
read -r -p "Type APPLY-008-010 to continue: " confirm
if [[ "$confirm" != "APPLY-008-010" ]]; then
  echo "Cancelled. Backup retained; no migrations were applied."
  exit 0
fi

docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
  sh -ec 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -f /docker-entrypoint-initdb.d/008_virtual_storage_bridge.sql'
docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
  sh -ec 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -f /docker-entrypoint-initdb.d/009_saas_tenant_hosting.sql'

docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
  sh -ec 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -f /docker-entrypoint-initdb.d/010_media_upload_claim.sql'

echo "Migrations 008 and 009 applied. Backup: $backup"
echo "IMPORTANT: this does not enable virtual storage or prove application-level isolation. Run the deployment verification and restore drill before enabling it."
