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

tmpdir="$(mktemp -d)"
chmod 700 "$tmpdir"
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

docker network create "$network" >/dev/null
docker run -d --rm --name "$minio" --network "$network" --network-alias restore-minio \
  -e MINIO_ROOT_USER="$restore_user" -e MINIO_ROOT_PASSWORD="$restore_password" \
  minio/minio:RELEASE-2025-09-07T16-13-09Z server /data --address ":9000" >/dev/null

export BACKUP_ID="$backup_id"
docker compose run --rm --no-deps --network "$network" \
  --volume "$tmpdir:/backup" --entrypoint /bin/sh object-init -ec '
    : "${BACKUP_S3_ENDPOINT:?Set BACKUP_S3_ENDPOINT in deploy/self-hosted/.env}"
    : "${BACKUP_S3_ACCESS_KEY:?Set BACKUP_S3_ACCESS_KEY in deploy/self-hosted/.env}"
    : "${BACKUP_S3_SECRET_KEY:?Set BACKUP_S3_SECRET_KEY in deploy/self-hosted/.env}"
    : "${BACKUP_S3_BUCKET:?Set BACKUP_S3_BUCKET in deploy/self-hosted/.env}"
    mc alias set backup "$BACKUP_S3_ENDPOINT" "$BACKUP_S3_ACCESS_KEY" "$BACKUP_S3_SECRET_KEY" --api S3v4
    mc cp "backup/$BACKUP_S3_BUCKET/$BACKUP_ID/postgres.dump" /backup/postgres.dump
    mc cp "backup/$BACKUP_S3_BUCKET/$BACKUP_ID/SHA256SUMS" /backup/SHA256SUMS
    mc cp "backup/$BACKUP_S3_BUCKET/$BACKUP_ID/manifest.json" /backup/manifest.json
    cd /backup
    sha256sum -c SHA256SUMS
    mc alias set restore http://restore-minio:9000 "$RESTORE_MINIO_USER" "$RESTORE_MINIO_PASSWORD" --api S3v4
    until mc admin info restore >/dev/null 2>&1; do sleep 2; done
    mc mb --ignore-existing restore/private-media
    mc mb --ignore-existing restore/public-media
    mc mirror --overwrite "backup/$BACKUP_S3_BUCKET/$BACKUP_ID/private-media" restore/private-media
    mc mirror --overwrite "backup/$BACKUP_S3_BUCKET/$BACKUP_ID/public-media" restore/public-media
    mc diff "backup/$BACKUP_S3_BUCKET/$BACKUP_ID/private-media" restore/private-media
    mc diff "backup/$BACKUP_S3_BUCKET/$BACKUP_ID/public-media" restore/public-media
  ' \
  -e RESTORE_MINIO_USER="$restore_user" -e RESTORE_MINIO_PASSWORD="$restore_password"

bash backup/restore-postgres-smoke.sh "$tmpdir/postgres.dump"
echo "Remote object snapshot restore smoke test: PASS (restored into disposable MinIO; source backup unchanged)."
