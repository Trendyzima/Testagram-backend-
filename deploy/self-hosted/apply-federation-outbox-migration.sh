#!/usr/bin/env bash
set -euo pipefail
umask 077

# Apply the native Federation outbox schema to an existing Backend database.
# PostgreSQL init hooks only run when the data volume is first initialized.
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
compose_file="$script_dir/docker-compose.yml"
env_file="$script_dir/.env"
backup_dir="${TESTAGRAM_BACKUP_DIR:-$script_dir/backups}"

if [[ ! -f "$env_file" ]]; then
  echo "Missing $env_file; copy .env.example to .env and configure it first." >&2
  exit 1
fi
for bin in docker pg_restore sha256sum; do
  command -v "$bin" >/dev/null 2>&1 || { echo "$bin is required" >&2; exit 1; }
done

mkdir -p "$backup_dir"
chmod 700 "$backup_dir"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
backup="$backup_dir/testagram-pre-federation-outbox-$stamp.dump"
checksum="$backup.sha256"

docker compose --env-file "$env_file" -f "$compose_file" up -d postgres
for attempt in $(seq 1 60); do
  if docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
    sh -ec 'pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB"' >/dev/null 2>&1; then
    break
  fi
  if [[ "$attempt" -eq 60 ]]; then
    echo "PostgreSQL did not become ready; no migration was applied." >&2
    exit 1
  fi
  sleep 2
done

# Never change an existing production schema without a verified pre-change dump.
docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
  sh -ec 'pg_dump --format=custom --no-owner --no-acl -U "$POSTGRES_USER" -d "$POSTGRES_DB"' > "$backup"
test -s "$backup"
pg_restore --list "$backup" >/dev/null
sha256sum "$backup" > "$checksum"
chmod 600 "$backup" "$checksum"

echo "Verified pre-migration backup: $backup"
echo "SHA-256: $(cut -d ' ' -f1 "$checksum")"
read -r -p "Type APPLY-FEDERATION-OUTBOX to apply the migration: " confirm
if [[ "$confirm" != "APPLY-FEDERATION-OUTBOX" ]]; then
  echo "Cancelled. The verified backup has been retained; no migration was applied."
  exit 0
fi

docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
  sh -ec 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -f /docker-entrypoint-initdb.d/022_federation_outbox.sql'

docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
  sh -ec 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -f /docker-entrypoint-initdb.d/024_federation_remote_follows.sql'

result="$(docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
  sh -ec 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atqc "SELECT to_regclass('\''testagram_local.federation_outbox'\'') IS NOT NULL AND to_regclass('\''testagram_local.remote_follows'\'') IS NOT NULL"')"
[[ "$result" == "t" ]] || { echo "Federation schema verification failed." >&2; exit 1; }

echo "Federation outbox and Backend-owned remote-follow migrations applied and verified."
echo "Retain the backup until the Backend and Federation workers pass production smoke tests."
