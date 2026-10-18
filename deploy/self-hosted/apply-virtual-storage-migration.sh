#!/usr/bin/env bash
set -euo pipefail

# Apply the additive virtual-storage bridge to an existing self-hosted database.
# Docker's /docker-entrypoint-initdb.d hooks only run for a new PostgreSQL volume;
# this explicit command is therefore required for already-initialized deployments.
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
compose_file="$script_dir/docker-compose.yml"
env_file="$script_dir/.env"

if [[ ! -f "$env_file" ]]; then
  echo "Missing $env_file; copy .env.example to .env and configure it first." >&2
  exit 1
fi

docker compose --env-file "$env_file" -f "$compose_file" up -d postgres

docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
  sh -ec 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -f /docker-entrypoint-initdb.d/008_virtual_storage_bridge.sql'

echo "Virtual-storage migration 008 applied. Keep VIRTUAL_STORAGE_ENABLED=false until the lifecycle release gates pass."
