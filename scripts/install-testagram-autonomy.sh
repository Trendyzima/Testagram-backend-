#!/usr/bin/env bash
set -Eeuo pipefail
umask 077
[[ "$(id -u)" -eq 0 ]] || { echo "Run as root: sudo bash scripts/install-testagram-autonomy.sh" >&2; exit 1; }
command -v systemctl >/dev/null 2>&1 || { echo "systemd is required" >&2; exit 1; }
command -v docker >/dev/null 2>&1 || { echo "Docker Engine is required" >&2; exit 1; }
docker compose version >/dev/null 2>&1 || { echo "Docker Compose v2 plugin is required" >&2; exit 1; }
root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
compose_dir="$root/deploy/self-hosted"
[[ -f "$compose_dir/docker-compose.yml" ]] || { echo "Compose file not found at $compose_dir" >&2; exit 1; }
[[ -f "$compose_dir/.env" ]] || { echo "Create $compose_dir/.env from .env.example and replace every placeholder first" >&2; exit 1; }
case "$compose_dir" in *'"'*|*$'\n'*|*$'\r'*) echo "Repository path contains unsupported quote/newline characters" >&2; exit 1 ;; esac
install -d -m 0755 /usr/local/sbin /etc/systemd/system /etc/default
install -m 0750 "$root/scripts/testagram-reconcile.sh" /usr/local/sbin/testagram-reconcile
install -m 0644 "$root/deploy/self-hosted/systemd/testagram-reconcile.service" /etc/systemd/system/testagram-reconcile.service
install -m 0644 "$root/deploy/self-hosted/systemd/testagram-reconcile.timer" /etc/systemd/system/testagram-reconcile.timer
install -d -m 0700 /var/lib/testagram-reconcile
printf 'TESTAGRAM_COMPOSE_DIR="%s"\n' "$compose_dir" > /etc/default/testagram-reconcile
chmod 0600 /etc/default/testagram-reconcile
systemctl daemon-reload
systemctl enable --now testagram-reconcile.timer
if ! systemctl start testagram-reconcile.service; then
  echo "Initial reconciliation failed; timer remains enabled and will retry. Inspect journalctl -u testagram-reconcile.service" >&2
  exit 1
fi
systemctl --no-pager --full status testagram-reconcile.timer
echo "Autonomy timer installed; retries every 60 seconds. No volumes are deleted and SSH/firewall rules are unchanged."
