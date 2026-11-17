#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

die() { printf 'production backup error: %s\n' "$*" >&2; exit 1; }

ROOT=/data/home/testagram
BACKEND="$ROOT/repos/backend"
[[ -d "$BACKEND/.git" ]] || die "the deployed Backend Git checkout is missing"
[[ -f "$BACKEND/deploy/self-hosted/.env" ]] || die "the production Compose environment file is missing"
[[ -n "${RESTIC_REPOSITORY:-}" ]] || die "RESTIC_REPOSITORY is required"
[[ -n "${RESTIC_PASSWORD:-}" ]] || die "RESTIC_PASSWORD is required"
[[ -n "${AWS_ACCESS_KEY_ID:-}" && -n "${AWS_SECRET_ACCESS_KEY:-}" ]] || die "dedicated S3 backup credentials are required"
[[ -n "${AWS_DEFAULT_REGION:-}" ]] || die "AWS_DEFAULT_REGION is required"

# Use a private subdirectory on InstaBox's persistent /data/tmp filesystem,
# not the RAM-backed /tmp tmpfs. Never chmod the shared parent directory.
TMP_ROOT="${TMPDIR:-/data/tmp}"
mkdir -p "$TMP_ROOT"
BACKUP_TMP_ROOT="$TMP_ROOT/testagram-production-backup"
mkdir -p "$BACKUP_TMP_ROOT"
chmod 700 "$BACKUP_TMP_ROOT"
export TMPDIR="$BACKUP_TMP_ROOT"

# Restic and jq are not guaranteed on every InstaBox template. Install
# only these small backup utilities when missing; do not upgrade the OS or
# restart production services as part of a backup.
if ! command -v restic >/dev/null 2>&1 || ! command -v jq >/dev/null 2>&1; then
  sudo -n apt-get update -qq
  sudo -n apt-get install -y restic jq
fi
command -v restic >/dev/null || die "restic installation did not produce a restic executable"
command -v jq >/dev/null || die "jq installation did not produce a jq executable"

export RESTIC_CACHE_DIR="${RESTIC_CACHE_DIR:-$TMPDIR/testagram-restic-cache}"
export BACKUP_STAGE_DIR="${BACKUP_STAGE_DIR:-$TMPDIR/testagram-backup-stage}"
mkdir -p "$RESTIC_CACHE_DIR" "$BACKUP_STAGE_DIR"
chmod 700 "$RESTIC_CACHE_DIR" "$BACKUP_STAGE_DIR"
cleanup() {
  rm -rf -- "$BACKUP_TMP_ROOT"
}
trap cleanup EXIT INT TERM

cd "$BACKEND"

# The machine's deployed checkout may predate this workflow. Refresh only the
# three backup utilities from main; do not reset the checkout or touch app code,
# Compose files, migrations, environment files, or running containers.
git fetch --depth 1 origin main
for file in scripts/self-hosted-backup.sh scripts/self-hosted-restore-drill.sh scripts/instabox-production-backup.sh; do
  git show FETCH_HEAD:"$file" > "$BACKEND/$file.new"
  chmod 700 "$BACKEND/$file.new"
  mv "$BACKEND/$file.new" "$BACKEND/$file"
done

# Initialize only an empty/uninitialized repository. Existing repositories are
# never reinitialized or overwritten; bad credentials still fail closed.
if ! restic snapshots --tag testagram >/dev/null 2>&1; then
  restic init
fi

./scripts/self-hosted-backup.sh backup

if [[ "${RUN_PRUNE:-false}" == "true" ]]; then
  ./scripts/self-hosted-backup.sh prune
fi

if [[ "${RUN_RESTORE_DRILL:-false}" == "true" ]]; then
  ./scripts/self-hosted-restore-drill.sh
fi

printf 'InstaBox production backup workflow completed successfully.\n'
