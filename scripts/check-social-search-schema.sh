#!/usr/bin/env bash
# Read-only schema/privilege preflight for the native Testagram post-search path.
# This script never starts containers or changes PostgreSQL state.
set -Eeuo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
compose_file="${COMPOSE_FILE:-$script_dir/../deploy/self-hosted/docker-compose.yml}"
env_file="${ENV_FILE:-$script_dir/../deploy/self-hosted/.env}"

[[ -f "$compose_file" && -f "$env_file" ]] || {
  echo "FAIL: Backend Compose file and .env are required." >&2
  exit 2
}
command -v docker >/dev/null 2>&1 || {
  echo "FAIL: docker is required." >&2
  exit 2
}

docker compose --env-file "$env_file" -f "$compose_file" config -q
docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
  sh -ec 'pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB"' >/dev/null

gaps="$(
  docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
    sh -ec 'psql -X -v ON_ERROR_STOP=1 -A -t -U "$POSTGRES_USER" -d "$POSTGRES_DB"' <<'SQL'
WITH required_relations(name) AS (
  VALUES
    ('testagram_local.accounts'),
    ('testagram_local.posts'),
    ('testagram_local.follows'),
    ('testagram_local.blocks'),
    ('testagram_local.mutes'),
    ('testagram_local.post_reactions'),
    ('testagram_local.replies'),
    ('testagram_local.reposts'),
    ('testagram_local.bookmarks'),
    ('testagram_local.notifications')
),
required_columns(table_name, column_name) AS (
  VALUES
    ('testagram_local.accounts','user_id'),
    ('testagram_local.accounts','username'),
    ('testagram_local.accounts','display_name'),
    ('testagram_local.accounts','avatar_key'),
    ('testagram_local.accounts','protected_account'),
    ('testagram_local.accounts','account_status'),
    ('testagram_local.posts','id'),
    ('testagram_local.posts','author_id'),
    ('testagram_local.posts','text'),
    ('testagram_local.posts','visibility'),
    ('testagram_local.posts','media_ids'),
    ('testagram_local.posts','language'),
    ('testagram_local.posts','created_at'),
    ('testagram_local.posts','deleted_at'),
    ('testagram_local.posts','likes_count'),
    ('testagram_local.posts','replies_count'),
    ('testagram_local.posts','reposts_count'),
    ('testagram_local.follows','follower_id'),
    ('testagram_local.follows','following_id'),
    ('testagram_local.follows','status'),
    ('testagram_local.blocks','blocker_id'),
    ('testagram_local.blocks','blocked_id'),
    ('testagram_local.mutes','muter_id'),
    ('testagram_local.mutes','muted_id')
),
gaps AS (
  SELECT 'missing relation: ' || name AS gap
  FROM required_relations
  WHERE to_regclass(name) IS NULL
  UNION ALL
  SELECT 'missing column: ' || table_name || '.' || column_name
  FROM required_columns
  WHERE to_regclass(table_name) IS NOT NULL
    AND NOT EXISTS (
      SELECT 1 FROM pg_attribute
      WHERE attrelid=to_regclass(table_name)
        AND attname=column_name
        AND attnum>0
        AND NOT attisdropped
    )
  UNION ALL
  SELECT 'missing schema USAGE privilege: ' || current_user
  WHERE to_regclass('testagram_local.accounts') IS NOT NULL
    AND NOT has_schema_privilege(current_user,'testagram_local','USAGE')
  UNION ALL
  SELECT 'missing SELECT privilege: ' || name
  FROM required_relations
  WHERE to_regclass(name) IS NOT NULL
    AND NOT has_table_privilege(current_user,name,'SELECT')
)
SELECT gap FROM gaps ORDER BY gap;
SQL
)"

if [[ -n "${gaps//[$'\\r\\n\\t ']/}" ]]; then
  printf 'FAIL: native post-search schema is incomplete for the configured PostgreSQL role.\n%s\n' "$gaps" >&2
  exit 1
fi

echo "PASS: native post-search relations, required columns and SELECT privileges are present."
