#!/usr/bin/env bash
# Backup-first, explicitly authorized application of the idempotent social-core
# migration to an existing PostgreSQL volume. Never runs during ordinary deploy.
set -Eeuo pipefail
umask 077

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
compose_file="${COMPOSE_FILE:-$script_dir/docker-compose.yml}"
env_file="${ENV_FILE:-$script_dir/.env}"
backup_dir="${BACKUP_DIR:-$script_dir/backups}"
migration_file="/docker-entrypoint-initdb.d/019_social_core.sql"
checker="$script_dir/../../scripts/check-social-search-schema.sh"

[[ -f "$compose_file" && -f "$env_file" && -f "$checker" ]] || {
  echo "Compose, .env and read-only schema checker are required." >&2
  exit 1
}
for bin in docker pg_restore sha256sum; do
  command -v "$bin" >/dev/null 2>&1 || { echo "$bin is required." >&2; exit 1; }
done

docker compose --env-file "$env_file" -f "$compose_file" config -q
docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
  sh -ec 'pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB"'
docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres test -r "$migration_file"

set +e
diagnostic="$(COMPOSE_FILE="$compose_file" ENV_FILE="$env_file" bash "$checker" 2>&1)"
diagnostic_status=$?
set -e
if [[ "$diagnostic_status" -eq 0 ]]; then
  echo "$diagnostic"
  echo "Social-core schema is already complete; no migration or backup was needed."
  exit 0
fi

printf '%s\n' "$diagnostic"
base_missing="$(
  docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
    sh -ec 'psql -X -v ON_ERROR_STOP=1 -A -t -U "$POSTGRES_USER" -d "$POSTGRES_DB"' <<'SQL'
SELECT string_agg(name, ', ' ORDER BY name)
FROM (VALUES
  ('testagram_local.accounts'),
  ('testagram_local.posts')
) AS required(name)
WHERE to_regclass(name) IS NULL;
SQL
)"
if [[ -n "$base_missing" ]]; then
  echo "ABORT: base native account/post tables are missing ($base_missing). This script applies only 019_social_core.sql; do not guess or replay the base migration." >&2
  exit 1
fi

mkdir -p "$backup_dir"
chmod 700 "$backup_dir"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
backup="$backup_dir/testagram-pre-social-core-$stamp.dump"
checksum="$backup.sha256"

docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
  sh -ec 'pg_dump --format=custom --no-owner --no-acl -U "$POSTGRES_USER" -d "$POSTGRES_DB"' > "$backup"
test -s "$backup"
pg_restore --list "$backup" >/dev/null
sha256sum "$backup" > "$checksum"
sha256sum -c "$checksum"
chmod 600 "$backup" "$checksum"
echo "Verified backup: $backup"

if [[ ! -t 0 ]]; then
  echo "ABORT: interactive confirmation is required. Backup retained; no migration applied." >&2
  exit 1
fi
read -r -p "Type APPLY-TESTAGRAM-SOCIAL-CORE to continue: " confirm
if [[ "$confirm" != "APPLY-TESTAGRAM-SOCIAL-CORE" ]]; then
  echo "Cancelled. Backup retained; no migration applied."
  exit 0
fi

docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
  sh -ec 'psql -X -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -f /docker-entrypoint-initdb.d/019_social_core.sql'

COMPOSE_FILE="$compose_file" ENV_FILE="$env_file" bash "$checker"
echo "Social-core migration applied and verified. No feature flag, DNS, Worker, image or production service was changed."
