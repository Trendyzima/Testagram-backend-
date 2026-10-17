#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

# Run from deploy/self-hosted. The restic repository must be off-host in production.
# Required environment: RESTIC_REPOSITORY and RESTIC_PASSWORD_FILE.
# Optional: RESTIC_CACHE_DIR, BACKUP_STAGE_DIR, RESTIC_KEEP_DAILY/WEEKLY/MONTHLY.

usage() {
  cat <<'USAGE'
Usage: ./scripts/self-hosted-backup.sh <backup|check|prune>

  backup  Export PostgreSQL and both MinIO media buckets, then store the snapshot
          in the encrypted restic repository.
  check   Verify repository structure and read a sample of stored data.
  prune   Apply the configured retention policy and prune unreferenced data.

Required:
  RESTIC_REPOSITORY   Restic repository (prefer off-host SFTP/S3/B2)
  RESTIC_PASSWORD_FILE  File containing the repository password (chmod 600)

Optional:
  RESTIC_KEEP_DAILY=7 RESTIC_KEEP_WEEKLY=5 RESTIC_KEEP_MONTHLY=12
  BACKUP_STAGE_DIR    Parent directory for temporary plaintext staging data
USAGE
}

die() { printf 'backup error: %s\n' "$*" >&2; exit 1; }
command -v docker >/dev/null || die "docker is required"
command -v restic >/dev/null || die "restic is required"
[[ -n "${RESTIC_REPOSITORY:-}" ]] || die "RESTIC_REPOSITORY is required"
[[ -n "${RESTIC_PASSWORD_FILE:-}" && -f "${RESTIC_PASSWORD_FILE}" ]] || die "RESTIC_PASSWORD_FILE must point to a readable password file"
[[ -r "$RESTIC_PASSWORD_FILE" ]] || die "RESTIC_PASSWORD_FILE is not readable"
[[ "$(stat -c '%a' "$RESTIC_PASSWORD_FILE" 2>/dev/null || stat -f '%Lp' "$RESTIC_PASSWORD_FILE" 2>/dev/null)" == "600" ]] || die "password file must have mode 600"
command -v docker >/dev/null
export RESTIC_CACHE_DIR="${RESTIC_CACHE_DIR:-${HOME}/.cache/restic-testagram}"

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
compose_dir="$(cd -- "$script_dir/../deploy/self-hosted" && pwd)"
cd "$compose_dir"
docker compose config --quiet

case "${1:-}" in
  backup)
    stage_parent="${BACKUP_STAGE_DIR:-${TMPDIR:-/tmp}}"
    mkdir -p "$stage_parent"
    chmod 700 "$stage_parent"
    stage="$(mktemp -d "$stage_parent/testagram-backup.XXXXXXXX")"
    chmod 700 "$stage"
    cleanup() { rm -rf -- "$stage"; }
    trap cleanup EXIT INT TERM

    printf 'Exporting PostgreSQL...\n'
    docker compose exec -T postgres sh -ec 'pg_dump -Fc --no-owner --no-acl -U "$POSTGRES_USER" -d "$POSTGRES_DB"' > "$stage/postgres.dump"
    [[ -s "$stage/postgres.dump" ]] || die "PostgreSQL dump is empty"
    docker compose exec -T postgres pg_restore --list < "$stage/postgres.dump" > "$stage/postgres.dump.list"
    chmod 600 "$stage/postgres.dump" "$stage/postgres.dump.list"

    printf 'Mirroring MinIO media buckets to protected staging...\n'
    mkdir -p "$stage/minio-private" "$stage/minio-public"
    chmod 700 "$stage/minio-private" "$stage/minio-public"
    docker compose run --rm --no-deps \
      -v "$stage:/backup" \
      --entrypoint /bin/sh object-init -ec '
        mc alias set local http://object-storage:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null
        mc mirror local/"$MINIO_BUCKET" /backup/minio-private
        mc mirror local/"$PUBLIC_MEDIA_BUCKET" /backup/minio-public
      '
    cat > "$stage/manifest.txt" <<EOF
Testagram self-hosted backup
created_utc=$(date -u +%Y-%m-%dT%H:%M:%SZ)
postgres_database=$(docker compose exec -T postgres sh -ec 'printf %s "$POSTGRES_DB"')
private_bucket=${MINIO_BUCKET:-testagram-private-media}
public_bucket=${PUBLIC_MEDIA_BUCKET:-testagram-public-media}
repository=${RESTIC_REPOSITORY}
EOF
    chmod 600 "$stage/manifest.txt"
    printf 'Writing encrypted restic snapshot...\n'
    restic backup --host testagram-self-hosted --tag testagram --tag automated "$stage"
    printf 'Verifying repository metadata and a data subset...\n'
    restic check --read-data-subset=1/20
    printf 'Backup complete. Temporary plaintext staging will now be removed.\n'
    ;;
  check)
    restic snapshots --tag testagram
    restic check --read-data-subset=1/20
    ;;
  prune)
    restic forget --tag testagram \
      --keep-daily "${RESTIC_KEEP_DAILY:-7}" \
      --keep-weekly "${RESTIC_KEEP_WEEKLY:-5}" \
      --keep-monthly "${RESTIC_KEEP_MONTHLY:-12}" --prune
    restic check
    ;;
  *)
    usage
    exit 2
    ;;
esac
