#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."
command -v docker >/dev/null
command -v sha256sum >/dev/null
docker compose config --quiet

# Fail before dumping data if the off-host destination is not configured.
docker compose run --rm --no-deps --entrypoint /bin/sh object-init -ec '
  : "${BACKUP_S3_ENDPOINT:?Set BACKUP_S3_ENDPOINT in deploy/self-hosted/.env}"
  : "${BACKUP_S3_ACCESS_KEY:?Set BACKUP_S3_ACCESS_KEY in deploy/self-hosted/.env}"
  : "${BACKUP_S3_SECRET_KEY:?Set BACKUP_S3_SECRET_KEY in deploy/self-hosted/.env}"
  : "${BACKUP_S3_BUCKET:?Set BACKUP_S3_BUCKET in deploy/self-hosted/.env}"
'

docker compose exec -T postgres pg_isready -q
BACKUP_ID="${BACKUP_ID:-$(date -u +%Y%m%dT%H%M%SZ)-$$}"
if [[ ! "$BACKUP_ID" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ ]]; then
  echo "BACKUP_ID contains unsupported characters." >&2
  exit 2
fi
export BACKUP_ID
export BACKUP_CREATED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

tmpdir="$(mktemp -d)"
chmod 700 "$tmpdir"
trap 'rm -rf "$tmpdir"' EXIT

docker compose exec -T postgres sh -ec 'exec pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" --format=custom' > "$tmpdir/postgres.dump"
test -s "$tmpdir/postgres.dump"
(
  cd "$tmpdir"
  sha256sum postgres.dump > SHA256SUMS
)
export BACKUP_POSTGRES_SHA256="$(awk '{print $1}' "$tmpdir/SHA256SUMS")"
export BACKUP_POSTGRES_BYTES="$(wc -c < "$tmpdir/postgres.dump" | tr -d ' ')"
printf '%s\n' "$BACKUP_ID" > "$tmpdir/SNAPSHOT.txt"

docker compose run --rm --no-deps \
  --volume "$tmpdir:/backup" \
  --entrypoint /bin/sh object-init -ec '
    : "${BACKUP_S3_ENDPOINT:?}"
    : "${BACKUP_S3_ACCESS_KEY:?}"
    : "${BACKUP_S3_SECRET_KEY:?}"
    : "${BACKUP_S3_BUCKET:?}"
    mc alias set local http://object-storage:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" --api S3v4
    mc alias set backup "$BACKUP_S3_ENDPOINT" "$BACKUP_S3_ACCESS_KEY" "$BACKUP_S3_SECRET_KEY" --api S3v4
    mc mb --ignore-existing "backup/$BACKUP_S3_BUCKET"
    mkdir -p /backup
    printf "{\"backupId\":\"%s\",\"createdAt\":\"%s\",\"postgresSha256\":\"%s\",\"postgresBytes\":%s,\"privateBucket\":\"%s\",\"publicBucket\":\"%s\"}\n" \
      "$BACKUP_ID" "$BACKUP_CREATED_AT" "$BACKUP_POSTGRES_SHA256" "$BACKUP_POSTGRES_BYTES" "$MINIO_BUCKET" "$PUBLIC_MEDIA_BUCKET" > /backup/manifest.json
    mc cp /backup/postgres.dump "backup/$BACKUP_S3_BUCKET/$BACKUP_ID/postgres.dump"
    mc cp /backup/SHA256SUMS "backup/$BACKUP_S3_BUCKET/$BACKUP_ID/SHA256SUMS"
    mc cp /backup/manifest.json "backup/$BACKUP_S3_BUCKET/$BACKUP_ID/manifest.json"
    mc mirror --overwrite "local/$MINIO_BUCKET" "backup/$BACKUP_S3_BUCKET/$BACKUP_ID/private-media"
    mc cp /backup/SNAPSHOT.txt "backup/$BACKUP_S3_BUCKET/$BACKUP_ID/private-media/SNAPSHOT.txt"
    mc mirror --overwrite "local/$PUBLIC_MEDIA_BUCKET" "backup/$BACKUP_S3_BUCKET/$BACKUP_ID/public-media"
    mc cp /backup/SNAPSHOT.txt "backup/$BACKUP_S3_BUCKET/$BACKUP_ID/public-media/SNAPSHOT.txt"
  '

printf 'Backup uploaded: %s\n' "$BACKUP_ID"
printf 'Database archive SHA-256: %s\n' "$BACKUP_POSTGRES_SHA256"
printf 'Destination: configured off-host S3-compatible backup bucket (credentials hidden).\n'
