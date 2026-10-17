#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."
command -v docker >/dev/null
command -v sha256sum >/dev/null
docker compose config --quiet

compose_env() {
  local key="$1"
  docker compose config --environment | awk -v key="$key" 'index($0,key"=")==1 { print substr($0,length(key)+2); found=1; exit } END { if (!found) exit 1 }'
}
repository="$(compose_env BACKUP_RESTIC_REPOSITORY)"
restic_password="$(compose_env BACKUP_RESTIC_PASSWORD)"
s3_access_key="$(compose_env BACKUP_S3_ACCESS_KEY)"
s3_secret_key="$(compose_env BACKUP_S3_SECRET_KEY)"
if [[ "$repository" != s3:https://* ]]; then
  echo "BACKUP_RESTIC_REPOSITORY must use an HTTPS S3-compatible endpoint." >&2
  exit 2
fi
if [[ -z "$restic_password" || -z "$s3_access_key" || -z "$s3_secret_key" ]]; then
  echo "Set BACKUP_RESTIC_PASSWORD, BACKUP_S3_ACCESS_KEY and BACKUP_S3_SECRET_KEY in .env." >&2
  exit 2
fi

docker compose exec -T postgres pg_isready -q
backup_id="${BACKUP_ID:-$(date -u +%Y%m%dT%H%M%SZ)-$$}"
if [[ ! "$backup_id" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ ]]; then
  echo "BACKUP_ID contains unsupported characters." >&2
  exit 2
fi
created_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
tmpdir="$(mktemp -d)"
chmod 700 "$tmpdir"
mkdir -m 700 "$tmpdir/staging"
printf '%s' "$restic_password" > "$tmpdir/restic-password"
chmod 600 "$tmpdir/restic-password"
trap 'rm -rf "$tmpdir"' EXIT

docker compose exec -T postgres sh -ec 'exec pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" --format=custom' > "$tmpdir/staging/postgres.dump"
test -s "$tmpdir/staging/postgres.dump"

docker compose run --rm --no-deps \
  --volume "$tmpdir/staging:/backup" \
  --entrypoint /bin/sh object-init -ec '
    mc alias set local http://object-storage:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" --api S3v4
    mkdir -p /backup/private-media /backup/public-media
    mc mirror --overwrite "local/$MINIO_BUCKET" /backup/private-media
    mc mirror --overwrite "local/$PUBLIC_MEDIA_BUCKET" /backup/public-media
  '

(
  cd "$tmpdir/staging"
  sha256sum postgres.dump > SHA256SUMS
)
postgres_sha="$(awk '{print $1}' "$tmpdir/staging/SHA256SUMS")"
postgres_bytes="$(wc -c < "$tmpdir/staging/postgres.dump" | tr -d ' ')"
private_objects="$(find "$tmpdir/staging/private-media" -type f | wc -l | tr -d ' ')"
public_objects="$(find "$tmpdir/staging/public-media" -type f | wc -l | tr -d ' ')"
printf '{"backupId":"%s","createdAt":"%s","postgresSha256":"%s","postgresBytes":%s,"privateObjectFiles":%s,"publicObjectFiles":%s}\n' \
  "$backup_id" "$created_at" "$postgres_sha" "$postgres_bytes" "$private_objects" "$public_objects" > "$tmpdir/staging/manifest.json"

restic_run() {
  docker run --rm \
    --volume "$tmpdir/staging:/data:ro" \
    --volume "$tmpdir/restic-password:/run/secrets/restic-password:ro" \
    --env RESTIC_REPOSITORY="$repository" \
    --env RESTIC_PASSWORD_FILE=/run/secrets/restic-password \
    --env AWS_ACCESS_KEY_ID="$s3_access_key" \
    --env AWS_SECRET_ACCESS_KEY="$s3_secret_key" \
    --env AWS_DEFAULT_REGION=us-east-1 \
    --env AWS_S3_FORCE_PATH_STYLE=true \
    restic/restic:0.18.1 "$@"
}
if ! restic_run snapshots --json >/dev/null 2>&1; then
  restic_run init
fi
restic_run backup /data --tag testagram --tag "$backup_id"
restic_run check
restic_run forget --tag testagram --keep-daily 7 --keep-weekly 4 --keep-monthly 6 --prune

printf 'Encrypted off-host backup created: %s\n' "$backup_id"
printf 'PostgreSQL archive SHA-256: %s\n' "$postgres_sha"
printf 'Files staged from private/public media buckets: %s/%s\n' "$private_objects" "$public_objects"
printf 'Restic repository: configured HTTPS S3-compatible destination (credentials hidden).\n'
