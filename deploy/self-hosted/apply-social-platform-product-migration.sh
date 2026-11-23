#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
compose_file="${COMPOSE_FILE:-$script_dir/docker-compose.yml}"
env_file="${ENV_FILE:-$script_dir/.env}"
backup_dir="${BACKUP_DIR:-$script_dir/backups}"
knowledge_migration_file="/docker-entrypoint-initdb.d/062_testagram_knowledge_engine.sql"
social_migration_file="/docker-entrypoint-initdb.d/063_testagram_social_platform_product_foundation.sql"

[[ -f "$compose_file" && -f "$env_file" ]] || { echo "Compose and .env files are required." >&2; exit 1; }
for bin in docker pg_restore sha256sum; do command -v "$bin" >/dev/null 2>&1 || { echo "$bin is required" >&2; exit 1; }; done

docker compose --env-file "$env_file" -f "$compose_file" config -q
docker compose --env-file "$env_file" -f "$compose_file" up -d postgres
for attempt in $(seq 1 60); do
  if docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres sh -ec 'pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB"' >/dev/null 2>&1; then break; fi
  if [[ "$attempt" -eq 60 ]]; then echo "PostgreSQL did not become ready; no migration applied." >&2; exit 1; fi
  sleep 2
done

schema_ready() {
  docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres sh -ec 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atqc "
    SELECT to_regclass('\''testagram_local.topics'\'') IS NOT NULL
       AND to_regclass('\''testagram_local.questions'\'') IS NOT NULL
       AND to_regclass('\''testagram_local.answers'\'') IS NOT NULL
       AND to_regclass('\''testagram_local.answer_revisions'\'') IS NOT NULL
       AND to_regclass('\''testagram_local.social_lists'\'') IS NOT NULL
       AND to_regclass('\''testagram_local.social_list_members'\'') IS NOT NULL
       AND to_regclass('\''testagram_local.social_list_posts'\'') IS NOT NULL
       AND to_regclass('\''testagram_local.post_drafts'\'') IS NOT NULL
       AND to_regclass('\''testagram_local.scheduled_posts'\'') IS NOT NULL
       AND to_regclass('\''testagram_local.stories'\'') IS NOT NULL
       AND to_regclass('\''testagram_local.social_reports'\'') IS NOT NULL
       AND to_regclass('\''testagram_local.social_moderation_actions'\'') IS NOT NULL
       AND to_regclass('\''testagram_local.social_moderation_appeals'\'') IS NOT NULL
  "'
}
if [[ "$(schema_ready)" == "t" ]]; then
  echo "Knowledge and social platform foundation schemas already exist; no changes made."
  exit 0
fi

docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres test -r "$knowledge_migration_file"
docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres test -r "$social_migration_file"
mkdir -p "$backup_dir"
chmod 700 "$backup_dir"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
backup="$backup_dir/testagram-pre-social-platform-$stamp.dump"
checksum="$backup.sha256"
docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres sh -ec 'pg_dump --format=custom --no-owner --no-acl -U "$POSTGRES_USER" -d "$POSTGRES_DB"' > "$backup"
test -s "$backup"
pg_restore --list "$backup" >/dev/null
sha256sum "$backup" > "$checksum"
sha256sum -c "$checksum"
chmod 600 "$backup" "$checksum"

echo "Verified backup: $backup"
read -r -p "Type APPLY-TESTAGRAM-SOCIAL-PLATFORM-FOUNDATION to continue: " confirm
if [[ "$confirm" != "APPLY-TESTAGRAM-SOCIAL-PLATFORM-FOUNDATION" ]]; then
  echo "Cancelled. Backup retained; no migration applied."
  exit 0
fi

docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres sh -ec 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -f "$1"' sh "$knowledge_migration_file"
docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres sh -ec 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -f "$1"' sh "$social_migration_file"
[[ "$(schema_ready)" == "t" ]] || { echo "Social platform foundation schema verification failed." >&2; exit 1; }
echo "Social platform foundation migration applied and verified. Retain backup until Backend and Flutter acceptance tests pass."
