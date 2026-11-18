#!/usr/bin/env bash
# Operator-run host inspection. This script runs locally on the InstaBox VPS;
# it does not call the InstaBox API or send commands through GitHub Actions.
# All Docker and PostgreSQL operations below are inspection/preflight only.
set -Eeuo pipefail
umask 077

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
backend_compose="${BACKEND_COMPOSE_FILE:-$repo_root/deploy/self-hosted/docker-compose.yml}"
backend_env="${BACKEND_ENV_FILE:-$repo_root/deploy/self-hosted/.env}"
data_compose="${DATA_CENTRE_COMPOSE_FILE:-/data/home/testagram/repos/data-centre/backend/docker-compose.yml}"
data_env="${DATA_CENTRE_ENV_FILE:-/data/home/testagram/repos/data-centre/backend/.env}"
identity_preflight="$repo_root/deploy/self-hosted/apply-native-identity-migrations.sh"

die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
need_file() { [[ -f "$1" ]] || die "Required file not found: $1"; }
as_root() {
  if [[ "$EUID" -eq 0 ]]; then "$@"; else sudo "$@"; fi
}

command -v docker >/dev/null 2>&1 || die "docker is required"
command -v jq >/dev/null 2>&1 || die "jq is required"
need_file "$backend_compose"
need_file "$backend_env"
need_file "$identity_preflight"

printf '%s\n' '=== TESTAGRAM VPS: OPERATOR-INITIATED READ-ONLY INSPECTION ==='
printf 'UTC time: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
printf '%s\n' 'No InstaBox API calls are made. No lifecycle or deployment actions are performed.'

# Validate Compose configuration without printing interpolated values.
as_root docker compose --env-file "$backend_env" -f "$backend_compose" config -q

printf '\n%s\n' '=== ALL TESTAGRAM COMPOSE CONTAINERS ==='
ids="$(
  {
    as_root docker ps -aq --no-trunc --filter label=com.docker.compose.project=testagram-self-hosted
    as_root docker ps -aq --no-trunc --filter label=com.docker.compose.project=testagram-data-centre
  } | sort -u
)"
[[ -n "$ids" ]] || die "No containers found for the Testagram Compose projects"

printf 'NAME\tIMAGE\tSTATE\tHEALTH\tRESTARTS\tEXIT_CODE\tOOM_KILLED\tSTARTED_AT\n'
bad=0
missing_health=0
while IFS= read -r id; do
  [[ -n "$id" ]] || continue
  row="$(as_root docker inspect --format '{{.Name}}	{{.Config.Image}}	{{.State.Status}}	{{if .State.Health}}{{.State.Health.Status}}{{else}}no-healthcheck{{end}}	{{.RestartCount}}	{{.State.ExitCode}}	{{.State.OOMKilled}}	{{.State.StartedAt}}' "$id")"
  printf '%s\n' "$row"
  state="$(as_root docker inspect --format '{{.State.Status}}' "$id")"
  health="$(as_root docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}no-healthcheck{{end}}' "$id")"
  if [[ "$state" != "running" || "$health" == "unhealthy" || "$health" == "starting" ]]; then bad=1; fi
  if [[ "$health" == "no-healthcheck" ]]; then missing_health=1; fi
done <<< "$ids"

printf '\n%s\n' '=== BACKEND COMPOSE SERVICES ==='
as_root docker compose --env-file "$backend_env" -f "$backend_compose" ps --all

printf '\n%s\n' '=== DATA CENTRE COMPOSE SERVICES ==='
if [[ -f "$data_compose" && -f "$data_env" ]]; then
  as_root docker compose --env-file "$data_env" -f "$data_compose" ps --all
else
  printf '%s\n' 'Data Centre Compose manifest/env not found at configured path; inspect separately if this service is expected on this host.'
fi

printf '\n%s\n' '=== DOCKER DAEMON ==='
as_root docker info --format 'ServerVersion={{.ServerVersion}} Containers={{.Containers}} Running={{.ContainersRunning}} Paused={{.ContainersPaused}} Stopped={{.ContainersStopped}}'

printf '\n%s\n' '=== DOCKER RESOURCE SNAPSHOT ==='
as_root docker stats --no-stream --format 'NAME={{.Name}} CPU={{.CPUPerc}} MEMORY={{.MemUsage}} MEM_PCT={{.MemPerc}}'

if [[ "$missing_health" -ne 0 ]]; then
  printf '%s\n' 'ACCEPTANCE GAP: one or more containers have no Docker healthcheck.'
  bad=1
fi

printf '\n%s\n' '=== NATIVE IDENTITY POSTGRESQL SCHEMA PREFLIGHT (READ-ONLY) ==='
# The helper runs config validation, pg_isready, and schema_ready SELECTs.
# With this variable explicitly empty, it exits before backup creation or
# migration application, even if the schema is incomplete.
if ! as_root env CONFIRM_NATIVE_IDENTITY_MIGRATIONS= bash "$identity_preflight"; then
  printf '%s\n' 'Identity schema is incomplete or its read-only preflight failed. No migrations were applied.' >&2
  bad=1
fi

printf '\n%s\n' '=== INSPECTION COMPLETE ==='
printf '%s\n' 'This script did not start, stop, restart, deploy, pull, recreate, prune, or modify containers.'
printf '%s\n' 'It did not apply database migrations or write application data.'
exit "$bad"
