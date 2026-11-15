#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

# Existing PostgreSQL volumes do not rerun docker-entrypoint-initdb.d.
# Require a verified pre-change dump and explicit operator authorization.
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
compose_file="${COMPOSE_FILE:-$script_dir/docker-compose.yml}"
env_file="${ENV_FILE:-$script_dir/.env}"
backup_dir="${BACKUP_DIR:-$script_dir/backups}"
migration_file="/docker-entrypoint-initdb.d/048_user_activity_watchdog.sql"

[[ -f "$compose_file" ]] || { echo "Missing Compose file: $compose_file" >&2; exit 1; }
[[ -f "$env_file" ]] || { echo "Missing environment file: $env_file" >&2; exit 1; }
for bin in docker pg_restore sha256sum; do
  command -v "$bin" >/dev/null 2>&1 || { echo "$bin is required" >&2; exit 1; }
done
docker compose --env-file "$env_file" -f "$compose_file" config -q
docker compose --env-file "$env_file" -f "$compose_file" up -d postgres

for attempt in $(seq 1 60); do
  if docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
    sh -ec 'pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB"' >/dev/null 2>&1; then break; fi
  if [[ "$attempt" -eq 60 ]]; then echo "PostgreSQL did not become ready; no migration was applied." >&2; exit 1; fi
  sleep 2
done

schema_ready() {
  docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
    sh -ec 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atqc "
      SELECT to_regclass('\''testagram_watchdog.impulses'\'') IS NOT NULL
         AND to_regclass('\''testagram_watchdog.service_leases'\'') IS NOT NULL
    "'
}
if [[ "$(schema_ready)" == "t" ]]; then
  echo "Watchdog schema is already installed; no changes made."
  exit 0
fi

docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres test -r "$migration_file"
mkdir -p "$backup_dir"
chmod 700 "$backup_dir"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
backup="$backup_dir/testagram-pre-watchdog-$stamp.dump"
checksum="$backup.sha256"

docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
  sh -ec 'pg_dump --format=custom --no-owner --no-acl -U "$POSTGRES_USER" -d "$POSTGRES_DB"' > "$backup"
test -s "$backup"
pg_restore --list "$backup" >/dev/null
sha256sum "$backup" > "$checksum"
sha256sum -c "$checksum"
chmod 600 "$backup" "$checksum"

echo "Verified pre-watchdog backup: $backup"
echo "The migration creates only the private watchdog queue/lease schema and enqueue function."
read -r -p "Type APPLY-USER-ACTIVITY-WATCHDOG to continue: " confirm
if [[ "$confirm" != "APPLY-USER-ACTIVITY-WATCHDOG" ]]; then
  echo "Cancelled. Backup retained; no migration was applied."
  exit 0
fi

docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
  sh -ec 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -f "$1"' sh "$migration_file"

[[ "$(schema_ready)" == "t" ]] || { echo "Watchdog schema verification failed." >&2; exit 1; }
echo "Watchdog schema installed and verified. Retain backup until staging activation and production smoke tests pass."
