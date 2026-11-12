#!/usr/bin/env bash
set -euo pipefail

# Existing PostgreSQL volumes do not rerun docker-entrypoint-initdb.d mounts.
# Take an off-container backup, verify it, then require explicit operator consent.
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
compose_file="${COMPOSE_FILE:-$script_dir/docker-compose.yml}"
env_file="${ENV_FILE:-$script_dir/.env}"
backup_dir="${BACKUP_DIR:-$script_dir/backups}"
mkdir -p "$backup_dir"
chmod 700 "$backup_dir"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
backup="$backup_dir/testagram-pre-media-replication-$stamp.dump"
checksum="$backup.sha256"

docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
  sh -ec 'pg_dump -Fc -U "$POSTGRES_USER" -d "$POSTGRES_DB"' > "$backup"
test -s "$backup"
sha256sum "$backup" > "$checksum"
sha256sum -c "$checksum"
pg_restore --list "$backup" >/dev/null

echo "Verified pre-migration backup: $backup"
echo "SHA-256: $(cut -d ' ' -f1 "$checksum")"
read -r -p "Type APPLY-MEDIA-REPLICATION to apply the additive ledger/outbox migrations: " confirm
if [[ "$confirm" != "APPLY-MEDIA-REPLICATION" ]]; then
  echo "Cancelled. Backup retained; no migrations were applied."
  exit 0
fi

docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
  sh -ec 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -f /docker-entrypoint-initdb.d/026_media_replica_ledger.sql'
docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
  sh -ec 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -f /docker-entrypoint-initdb.d/027_media_replication_outbox.sql'
docker compose --env-file "$env_file" -f "$compose_file" exec -T postgres \
  sh -ec 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atc "SELECT to_regclass('''testagram_media.media_replication_outbox''') IS NOT NULL AND EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='''media_replication_enqueue_after_asset_write''' AND NOT tgisinternal)"' \
  | grep -qx t

echo "Media replication ledger and transactional outbox applied and verified."
echo "Replication remains disabled until independent R2/B2 credentials and object-origin regression tests are configured."
