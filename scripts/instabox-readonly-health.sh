#!/usr/bin/env bash
# Read-only Instabox production inspection. This script never starts, stops,
# restarts, recreates, pulls or mutates containers, networks, volumes or data.
set -euo pipefail

ROOT="${TESTAGRAM_ROOT:-/data/home/testagram}"
BACKEND_COMPOSE="$ROOT/repos/backend/deploy/self-hosted/docker-compose.yml"
BACKEND_ENV="$ROOT/repos/backend/deploy/self-hosted/.env"
DC_COMPOSE="${DATA_CENTRE_COMPOSE:-$ROOT/repos/data-centre/backend/docker-compose.yml}"
DC_ENV="${DATA_CENTRE_ENV:-$ROOT/repos/data-centre/backend/.env}"
failures=0
unknown=0

fail() {
  printf 'FAIL  %s\n' "$1"
  failures=$((failures + 1))
}

pass() {
  printf 'PASS  %s\n' "$1"
}

section() {
  printf '\n=== %s ===\n' "$1"
}

section "Host and Docker availability"
if ! command -v sudo >/dev/null 2>&1 || ! sudo docker info >/dev/null 2>&1; then
  fail "Docker daemon is not accessible; no recovery action was attempted"
  exit 1
fi
pass "Docker daemon is reachable"

section "Compose project inventory"
declare -A seen_container_ids=()
container_ids=()
for pair in "Backend|$BACKEND_COMPOSE|$BACKEND_ENV" "Data Centre|$DC_COMPOSE|$DC_ENV"; do
  IFS='|' read -r label compose_file env_file <<< "$pair"
  if [[ ! -f "$compose_file" ]]; then
    fail "$label Compose file missing: $compose_file"
    continue
  fi
  if [[ ! -f "$env_file" ]]; then
    fail "$label environment file missing: $env_file"
    continue
  fi
  pass "$label Compose and environment files exist"
  compose_args=(--env-file "$env_file" -f "$compose_file")
  if ! sudo docker compose "${compose_args[@]}" ps --all; then
    fail "$label Compose inventory could not be read"
    continue
  fi
  config="$(sudo docker compose "${compose_args[@]}" config --format json)"
  mapfile -t expected_services < <(jq -r '.services | to_entries[] | select((.value.profiles // []) | length == 0) | .key' <<<"$config")
  mapfile -t present_services < <(sudo docker compose "${compose_args[@]}" ps --all --services | sort -u)
  for service in "${expected_services[@]}"; do
    if ! printf '%s\n' "${present_services[@]}" | grep -Fxq "$service"; then
      fail "$label service $service has no container"
    fi
  done
  mapfile -t stack_ids < <(sudo docker compose "${compose_args[@]}" ps --all -q)
  for id in "${stack_ids[@]}"; do
    [[ -z "$id" ]] && continue
    if [[ -z "${seen_container_ids[$id]:-}" ]]; then
      seen_container_ids["$id"]=1
      container_ids+=("$id")
    fi
  done
done

section "Every Backend and Data Centre container state"
if (( ${#container_ids[@]} == 0 )); then
  fail "No Testagram containers were found"
else
  for id in "${container_ids[@]}"; do
    state="$(sudo docker inspect --format '{{.Name}}|{{.State.Status}}|{{if .State.Health}}{{.State.Health.Status}}{{else}}no-healthcheck{{end}}|{{.RestartCount}}|{{.State.ExitCode}}' "$id")"
    IFS='|' read -r name status health restarts exit_code <<< "$state"
    printf '%-42s state=%-10s health=%-16s restarts=%s exit=%s\n' "$name" "$status" "$health" "$restarts" "$exit_code"
    if [[ "$status" == "running" && "$health" == "healthy" ]]; then
      pass "${name#/} is running and healthy"
    elif [[ "$status" == "running" && "$health" == "no-healthcheck" ]]; then
      unknown=$((unknown + 1))
      fail "${name#/} is running without a Docker healthcheck"
    elif [[ "$status" == "running" ]]; then
      fail "${name#/} is running but health is $health"
    elif [[ "$exit_code" == "0" ]]; then
      printf 'INFO  %s is a stopped zero-exit container (likely a one-shot job)\n' "$name"
    else
      fail "${name#/} is stopped with exit code $exit_code"
    fi
  done
fi

check_http() {
  local label="$1"
  local url="$2"
  local status
  status="$(curl --silent --show-error --max-time 15 -o /dev/null -w '%{http_code}' "$url" 2>/dev/null || true)"
  if [[ "$status" == "200" ]]; then
    pass "$label returned HTTP 200"
  else
    fail "$label returned HTTP ${status:-no-response}"
  fi
}

section "Public HTTPS service probes"
check_http "Backend liveness" "https://api.testagram.site/healthz"
check_http "Backend readiness" "https://api.testagram.site/readyz"
check_http "Go CDN liveness" "https://media.testagram.site/healthz"
check_http "Go CDN readiness" "https://media.testagram.site/readyz"
check_http "Staff console liveness" "https://ops.testagram.site/healthz"
check_http "Federation liveness" "https://federation.testagram.site/healthz"
check_http "Federation readiness" "https://federation.testagram.site/readyz"

section "Private origin probes"
if curl --silent --show-error --max-time 10 -fsS http://127.0.0.1:9000/minio/health/live >/dev/null 2>&1; then
  pass "Private MinIO liveness"
else
  fail "Private MinIO liveness failed"
fi

section "Read-only acceptance summary"
printf 'Failures: %s\nContainers without healthchecks: %s\n' "$failures" "$unknown"
if (( failures > 0 )); then
  printf 'RESULT: NOT HEALTHY — no automatic restart or repair was attempted.\n'
  exit 1
fi
printf 'RESULT: All inspected checks passed. This is a point-in-time inspection, not a soak test.\n'
