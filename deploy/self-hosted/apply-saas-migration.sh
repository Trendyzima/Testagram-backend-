#!/usr/bin/env bash
set -euo pipefail

# Apply the additive multi-tenant control-plane schema to an existing VPS DB.
# PostgreSQL init scripts run only when the data volume is first created, so
# existing installations must run this explicit migration command once.
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
compose_file="$script_dir/docker-compose.yml"
env_file="$script_dir/.env"

if [[ ! -f "$env_file" ]]; then
  echo "Missing $env_file; copy .env.example to .env and configure it first." >&2
  exit 1
fi

docker compose --env-file "$env_file" -f "$compose_file" up -d postgres
docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
  sh -ec 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -f /docker-entrypoint-initdb.d/009_saas_tenant_hosting.sql'

echo "Migration 009 applied. This creates backend-only tenancy/metering tables; it does not expose rental APIs or enable billing."
