#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

# Run from the repository root or any directory. Use an off-host restic repository in production.
# Required environment: RESTIC_REPOSITORY and either RESTIC_PASSWORD_FILE or RESTIC_PASSWORD.
# Optional: RESTIC_CACHE_DIR, BACKUP_STAGE_DIR, RESTIC_KEEP_DAILY/WEEKLY/MONTHLY.

usage() {
  cat <<'USAGE'
Usage: ./scripts/self-hosted-backup.sh <backup|check|prune>

  backup  Export PostgreSQL and all four MinIO buckets, then store the snapshot
          in the encrypted restic repository.
  check   Verify repository structure and read a sample of stored data.
  prune   Apply the configured retention policy and prune unreferenced data.

Required:
  RESTIC_REPOSITORY   Restic repository (prefer off-host SFTP/S3/B2)
  RESTIC_PASSWORD_FILE  File containing the repository password (chmod 600)
  RESTIC_PASSWORD       Alternative for short-lived automated jobs; never log it

Optional:
  RESTIC_KEEP_DAILY=7 RESTIC_KEEP_WEEKLY=5 RESTIC_KEEP_MONTHLY=12
  BACKUP_STAGE_DIR    Parent directory for temporary plaintext staging data
USAGE
}

die() { printf 'backup error: %s\n' "$*" >&2; exit 1; }
command -v docker >/dev/null || die "docker is required"
command -v restic >/dev/null || die "restic is required"
[[ -n "${RESTIC_REPOSITORY:-}" ]] || die "RESTIC_REPOSITORY is required"
if [[ -n "${RESTIC_PASSWORD_FILE:-}" ]]; then
  [[ -f "$RESTIC_PASSWORD_FILE" && -r "$RESTIC_PASSWORD_FILE" ]] || die "RESTIC_PASSWORD_FILE must point to a readable password file"
  [[ "$(stat -c '%a' "$RESTIC_PASSWORD_FILE" 2>/dev/null || stat -f '%Lp' "$RESTIC_PASSWORD_FILE" 2>/dev/null)" == "600" ]] || die "password file must have mode 600"
elif [[ -z "${RESTIC_PASSWORD:-}" ]]; then
  die "set RESTIC_PASSWORD_FILE or RESTIC_PASSWORD"
fi

# InstaBox's standard template may expose Docker through sudo rather than the
# user's supplementary groups. Prefer unprivileged Docker, then use passwordless
# sudo only when needed; never change Docker group membership during a backup.
DOCKER=(docker)
if ! docker info >/dev/null 2>&1; then
  command -v sudo >/dev/null || die "Docker is not accessible and sudo is unavailable"
  sudo -n docker info >/dev/null 2>&1 || die "Docker is not accessible to this user or passwordless sudo"
  DOCKER=(sudo -n docker)
fi
export RESTIC_CACHE_DIR="${RESTIC_CACHE_DIR:-${HOME}/.cache/restic-testagram}"

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
compose_dir="$(cd -- "$script_dir/../deploy/self-hosted" && pwd)"
cd "$compose_dir"

# Do not claim a complete backup if writes have moved to an external provider.
# This helper captures the local PostgreSQL + MinIO stack only; active B2/R2
# buckets require their own explicit backup and restore-tested targets.
read_env_flag() {
  local key="$1"
  awk -F= -v key="$key" '$1 == key {
    value = $2
    gsub(/^[[:space:]"]+|[[:space:]"]+$/, "", value)
  } END { print tolower(value) }' .env
}
for flag in DATA_CENTRE_MEDIA_ENABLED MEDIA_REPLICATION_ENABLED FRESH_MEDIA_ENABLED; do
  value="$(read_env_flag "$flag")"
  if [[ "$value" == "true" || "$value" == "1" ]]; then
    die "$flag is enabled; extend the backup to active external object-store buckets before relying on it"
  fi
done
"${DOCKER[@]}" compose config --quiet

case "${1:-}" in
  backup)
    stage_parent="${BACKUP_STAGE_DIR:-${TMPDIR:-/tmp}}"
    mkdir -p "$stage_parent"
    stage=$(mktemp -d "${stage_parent%/}/testagram-backup.XXXXXXXX")
    chmod 700 "$stage"
    cleanup() { rm -rf -- "$stage"; }
    trap cleanup EXIT INT TERM

    printf 'Exporting PostgreSQL...\n'
    # shellcheck disable=SC2016
    "${DOCKER[@]}" compose exec -T postgres sh -ec 'pg_dump -Fc --no-owner --no-acl -U "$POSTGRES_USER" -d "$POSTGRES_DB"' > "$stage/postgres.dump"
    [[ -s "$stage/postgres.dump" ]] || die "PostgreSQL dump is empty"
    "${DOCKER[@]}" compose exec -T postgres pg_restore --list < "$stage/postgres.dump" > "$stage/postgres.dump.list"
    chmod 600 "$stage/postgres.dump" "$stage/postgres.dump.list"

    printf 'Mirroring all MinIO buckets to protected staging...\n'
    mkdir -p "$stage/minio-private" "$stage/minio-public" "$stage/minio-identity-evidence" "$stage/minio-metadata"
    chmod 700 "$stage/minio-private" "$stage/minio-public" "$stage/minio-identity-evidence" "$stage/minio-metadata"
    # shellcheck disable=SC2016
    "${DOCKER[@]}" compose run --rm --no-deps \
      -v "$stage:/backup" \
      --entrypoint /bin/sh object-init -ec '
        mc alias set local http://object-storage:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null
        mc mirror --preserve local/"$MINIO_BUCKET" /backup/minio-private
        mc mirror --preserve local/"$PUBLIC_MEDIA_BUCKET" /backup/minio-public
        mc mirror --preserve local/"$IDENTITY_EVIDENCE_PRIVATE_BUCKET" /backup/minio-identity-evidence
        mc mirror --preserve local/"$MINIO_METADATA_BUCKET" /backup/minio-metadata
      '
    printf '%s\n' \
      "Testagram self-hosted backup" \
      "created_utc=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      "postgres_database=$("${DOCKER[@]}" compose exec -T postgres printenv POSTGRES_DB | tr -d '\r')" \
      "private_bucket=${MINIO_BUCKET:-testagram-private-media}" \
      "public_bucket=${PUBLIC_MEDIA_BUCKET:-testagram-public-media}" \
      "identity_evidence_bucket=${IDENTITY_EVIDENCE_PRIVATE_BUCKET:-testagram-identity-evidence-private}" \
      "metadata_bucket=${MINIO_METADATA_BUCKET:-testagram-media-metadata}" \
      "repository=${RESTIC_REPOSITORY}" > "$stage/manifest.txt"
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
