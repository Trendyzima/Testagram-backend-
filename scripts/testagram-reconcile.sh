#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

# Reconcile the local Compose stack after boot, daemon recovery, or network return.
# Never delete volumes, prune images, edit application code, or change host firewall policy.

log() {
  printf 'testagram-reconcile: %s\n' "$*" >&2
  if command -v logger >/dev/null 2>&1; then logger -t testagram-reconcile -- "$*" || true; fi
}
die() { log "ERROR: $*"; exit 1; }

compose_dir="${TESTAGRAM_COMPOSE_DIR:-/opt/testagram/deploy/self-hosted}"
state_dir="${TESTAGRAM_RECONCILE_STATE_DIR:-/var/lib/testagram-reconcile}"
lock_file="${TESTAGRAM_RECONCILE_LOCK_FILE:-/run/lock/testagram-reconcile.lock}"
mkdir -p "$(dirname "$lock_file")" "$state_dir"
chmod 700 "$state_dir"
exec 9>"$lock_file"
flock -n 9 || exit 0

[[ -d "$compose_dir" ]] || die "Compose directory not found: $compose_dir"
[[ -f "$compose_dir/docker-compose.yml" ]] || die "docker-compose.yml missing in $compose_dir"
[[ -f "$compose_dir/.env" ]] || die ".env missing in $compose_dir; refusing to start with incomplete configuration"
command -v docker >/dev/null 2>&1 || die "docker CLI is not installed"
docker info >/dev/null 2>&1 || die "Docker daemon is unavailable; next timer tick will retry"
docker compose version >/dev/null 2>&1 || die "Docker Compose plugin is unavailable"

cd "$compose_dir"
docker compose config --quiet || die "Compose configuration is invalid; inspect journal before changing production config"
log "Reconciling Compose services in $compose_dir"
# Idempotently starts stopped services and retries missing-image pulls. Never runs
# down -v, prune, or removes persistent volumes.
docker compose up -d --remove-orphans || die "Compose reconciliation failed; retry scheduled by systemd timer"

# Docker restart policies handle exited processes. Docker does not restart a
# process solely because its health status becomes unhealthy. Bound such restarts.
now="$(date +%s)"
mapfile -t container_ids < <(docker ps -aq --filter label=com.docker.compose.project=testagram-self-hosted)
for id in "${container_ids[@]}"; do
  [[ -n "$id" ]] || continue
  health="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$id" 2>/dev/null || printf 'missing')"
  [[ "$health" == "unhealthy" ]] || continue

  state_file="$state_dir/$id.restarts"
  touch "$state_file"
  recent_count="$(awk -v cutoff="$((now - 1800))" '$1 >= cutoff {n++} END {print n+0}' "$state_file")"
  if (( recent_count >= 3 )); then
    log "ALERT: container $id exhausted 3 health restarts in 30 minutes; leaving it for operator investigation"
    continue
  fi

  printf '%s\n' "$now" >> "$state_file"
  tmp="$state_file.tmp"
  awk -v cutoff="$((now - 1800))" '$1 >= cutoff {print $1}' "$state_file" > "$tmp"
  mv "$tmp" "$state_file"
  log "Container $id is unhealthy; bounded recovery restart $((recent_count + 1))/3"
  docker restart "$id" >/dev/null || log "WARNING: restart failed for container $id"
done

# Connectivity is diagnostic only: cached/local services still start offline.
# The timer retries failed image pulls and reconciliation after network return.
if ! ip route show default 2>/dev/null | grep -q .; then
  log "No default route detected; local services were reconciled and network recovery will be retried"
fi
log "Reconciliation finished"
