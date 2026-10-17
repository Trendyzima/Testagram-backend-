#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."
backup_id="${1:-${BACKUP_ID:-}}"
if [[ -z "$backup_id" || ! "$backup_id" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ ]]; then
  echo "Usage: bash backup/restore-smoke.sh BACKUP_ID" >&2
  exit 2
fi
command -v docker >/dev/null
docker compose config --quiet

compose_env() {
  local key="$1"
  docker compose config --environment | awk -v key="$key" 'index($0,key"=")==1 { print substr($0,length(key)+2); found=1; exit } END { if (!found) exit 1 }'
}
repository="$(compose_env BACKUP_RESTIC_REPOSITORY)"
restic_password="$(compose_env BACKUP_RESTIC_PASSWORD)"
s3_access_key="$(compose_env BACKUP_S3_ACCESS_KEY)"
s3_secret_key="$(compose_env BACKUP_S3_SECRET_KEY)"
if [[ "$repository" != s3:https://* || -z "$restic_password" || -z "$s3_access_key" || -z "$s3_secret_key" ]]; then
  echo "Configure the HTTPS Restic repository and credentials in .env before restoring." >&2
  exit 2
fi

tmpdir="$(mktemp -d)"
chmod 700 "$tmpdir"
mkdir -m 700 "$tmpdir/restore"
printf '%s' "$restic_password" > "$tmpdir/restic-password"
chmod 600 "$tmpdir/restic-password"
network="testagram-restore-net-$$"
minio="testagram-restore-minio-$$"
restore_user="restoreadmin"
restore_password="$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')"
cleanup() {
  docker rm -f "$minio" >/dev/null 2>&1 || true
  docker network rm "$network" >/dev/null 2>&1 || true
  rm -rf "$tmpdir"
}
trap cleanup EXIT

docker run --rm \
  --volume "$tmpdir/restore:/restore" \
  --volume "$tmpdir/restic-password:/run/secrets/restic-password:ro" \
  --env RESTIC_REPOSITORY="$repository" \
  --env RESTIC_PASSWORD_FILE=/run/secrets/restic-password \
  --env AWS_ACCESS_KEY_ID="$s3_access_key" \
  --env AWS_SECRET_ACCESS_KEY="$s3_secret_key" \
  --env AWS_DEFAULT_REGION=us-east-1 \
  --env AWS_S3_FORCE_PATH_STYLE=true \
  restic/restic:0.18.1 restore latest --tag "$backup_id" --target /restore

manifest="$(find "$tmpdir/restore" -type f -name manifest.json -print -quit)"
if [[ -z "$manifest" ]]; then
  echo "Restored snapshot is missing manifest.json." >&2
  exit 1
fi
root="$(dirname "$manifest")"
(
  cd "$root"
  sha256sum -c SHA256SUMS
)
bash backup/restore-postgres-smoke.sh "$root/postgres.dump"

# Rehydrate the object snapshot into a disposable MinIO instance with no host ports.
docker network create "$network" >/dev/null
docker run -d --rm --name "$minio" --network "$network" --network-alias restore-minio \
  -e MINIO_ROOT_USER="$restore_user" -e MINIO_ROOT_PASSWORD="$restore_password" \
  minio/minio:RELEASE-2025-09-07T16-13-09Z server /data --address ":9000" >/dev/null

docker compose run --rm --no-deps --network "$network" \
  --volume "$root:/backup:ro" --entrypoint /bin/sh \
  -e RESTORE_MINIO_USER="$restore_user" -e RESTORE_MINIO_PASSWORD="$restore_password" \
  object-init -ec '
    mc alias set restore http://restore-minio:9000 "$RESTORE_MINIO_USER" "$RESTORE_MINIO_PASSWORD" --api S3v4
    until mc admin info restore >/dev/null 2>&1; do sleep 2; done
    mc mb --ignore-existing restore/private-media
    mc mb --ignore-existing restore/public-media
    mc mirror --overwrite /backup/private-media restore/private-media
    mc mirror --overwrite /backup/public-media restore/public-media
    mc diff /backup/private-media restore/private-media
    mc diff /backup/public-media restore/public-media
  '

echo "Restore smoke test: PASS (PostgreSQL restored in isolation; private/public objects rehydrated into disposable MinIO)."
