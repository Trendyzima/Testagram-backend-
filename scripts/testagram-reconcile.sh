#!/usr/bin/env bash
set -Eeuo pipefail
umask 077
log() {
  printf 'testagram-reconcile: %s\n' "$*" >&2
  if command -v logger >/dev/null 2>&1; then
    logger -t testagram-reconcile -- "$*" || true
  fi
}
die(){ log "ERROR: $*"; exit 1; }
compose_dir="${TESTAGRAM_COMPOSE_DIR:-/opt/testagram/deploy/self-hosted}"
state_dir="${TESTAGRAM_RECONCILE_STATE_DIR:-/var/lib/testagram-reconcile}"
lock_file="${TESTAGRAM_RECONCILE_LOCK_FILE:-/run/lock/testagram-reconcile.lock}"
mkdir -p "$(dirname "$lock_file")" "$state_dir"; chmod 700 "$state_dir"
exec 9>"$lock_file"; flock -n 9 || exit 0
[[ -d "$compose_dir" ]] || die "Compose directory not found: $compose_dir"
[[ -f "$compose_dir/docker-compose.yml" ]] || die "docker-compose.yml missing in $compose_dir"
[[ -f "$compose_dir/.env" ]] || die ".env missing; refusing to start with incomplete configuration"
command -v docker >/dev/null 2>&1 || die "docker CLI is not installed"
docker info >/dev/null 2>&1 || die "Docker daemon unavailable; next timer tick will retry"
docker compose version >/dev/null 2>&1 || die "Docker Compose plugin unavailable"
cd "$compose_dir"
docker compose config --quiet || die "Compose configuration is invalid; inspect journal"
log "Reconciling Compose services in $compose_dir"
docker compose up -d --remove-orphans || die "Compose reconciliation failed; timer will retry"
# Restart unhealthy containers only with a bounded budget; never delete data.
now="$(date +%s)"
mapfile -t ids < <(docker ps -aq --filter label=com.docker.compose.project=testagram-self-hosted)
for id in "${ids[@]}"; do
  [[ -n "$id" ]] || continue
  health="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$id" 2>/dev/null || printf 'missing')"
  [[ "$health" == "unhealthy" ]] || continue
  state="$state_dir/$id.restarts"; touch "$state"
  count="$(awk -v cutoff="$((now-1800))" '$1 >= cutoff {n++} END {print n+0}' "$state")"
  if (( count >= 3 )); then log "ALERT: $id exhausted 3 health restarts in 30 minutes; operator investigation required"; continue; fi
  printf '%s\n' "$now" >> "$state"; tmp="$state.tmp"
  awk -v cutoff="$((now-1800))" '$1 >= cutoff {print $1}' "$state" > "$tmp"; mv "$tmp" "$state"
  log "Restarting unhealthy container $id ($((count+1))/3 in 30 minutes)"
  docker restart "$id" >/dev/null || log "WARNING: restart failed for $id"
done
if ! ip route show default 2>/dev/null | grep -q .; then log "No default route detected; local services reconciled and timer will retry after network recovery"; fi
log "Reconciliation finished"
