#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

die() { printf 'production backup error: %s\n' "$*" >&2; exit 1; }

ROOT=/data/home/testagram
BACKEND="$ROOT/repos/backend"
[[ -d "$BACKEND/.git" ]] || die "the deployed Backend Git checkout is missing"
command -v flock >/dev/null || die "flock is required to prevent overlapping production backup runs"
command -v realpath >/dev/null || die "realpath is required to validate persistent backup staging"
exec 9>"$ROOT/production-backup.lock"
flock -n 9 || die "another Testagram production backup is already running"
[[ -f "$BACKEND/deploy/self-hosted/.env" ]] || die "the production Compose environment file is missing"
[[ -n "${RESTIC_REPOSITORY:-}" ]] || die "RESTIC_REPOSITORY is required"
[[ -n "${RESTIC_PASSWORD:-}" ]] || die "RESTIC_PASSWORD is required"
[[ -n "${AWS_ACCESS_KEY_ID:-}" && -n "${AWS_SECRET_ACCESS_KEY:-}" ]] || die "dedicated S3 backup credentials are required"
[[ -n "${AWS_DEFAULT_REGION:-}" ]] || die "AWS_DEFAULT_REGION is required"

# Use a private subdirectory on InstaBox's persistent /data/tmp filesystem,
# not the RAM-backed /tmp tmpfs. Never chmod the shared parent directory.
TMP_ROOT="${BACKUP_TMP_PARENT:-/data/tmp}"
mkdir -p "$TMP_ROOT"
TMP_ROOT="$(realpath "$TMP_ROOT")"
case "$TMP_ROOT" in
  /data|/data/*) ;;
  *) die "backup staging parent must be on InstaBox persistent /data storage, not tmpfs" ;;
esac
BACKUP_TMP_ROOT="$TMP_ROOT/testagram-production-backup"
# A prior hard kill may have bypassed EXIT cleanup. The machine-side lock is
# already held, so remove only this dedicated backup scratch directory.
rm -rf -- "$BACKUP_TMP_ROOT"
mkdir -p "$BACKUP_TMP_ROOT"
chmod 700 "$BACKUP_TMP_ROOT"
export TMPDIR="$BACKUP_TMP_ROOT"

# Restic, jq and Python are not guaranteed on every InstaBox template. Install
# only these small backup utilities when missing; do not upgrade the OS or
# restart production services as part of a backup.
if ! command -v restic >/dev/null 2>&1 || ! command -v jq >/dev/null 2>&1 || ! command -v python3 >/dev/null 2>&1; then
  sudo -n apt-get update -qq
  sudo -n apt-get install -y restic jq python3
fi
command -v restic >/dev/null || die "restic installation did not produce a restic executable"
command -v jq >/dev/null || die "jq installation did not produce a jq executable"
command -v python3 >/dev/null || die "python3 is required for federation database validation"

export RESTIC_CACHE_DIR="${RESTIC_CACHE_DIR:-$TMPDIR/testagram-restic-cache}"
export BACKUP_STAGE_DIR="${BACKUP_STAGE_DIR:-$TMPDIR/testagram-backup-stage}"
mkdir -p "$RESTIC_CACHE_DIR" "$BACKUP_STAGE_DIR"
chmod 700 "$RESTIC_CACHE_DIR" "$BACKUP_STAGE_DIR"
cleanup() {
  rm -rf -- "$BACKUP_TMP_ROOT"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

cd "$BACKEND"

# The machine's deployed checkout may predate this workflow. Refresh only the
# three backup utilities from main; do not reset the checkout or touch app code,
# Compose files, migrations, environment files, or running containers.
GIT_TERMINAL_PROMPT=0 git fetch --depth 1 origin main
for file in scripts/self-hosted-backup.sh scripts/self-hosted-restore-drill.sh scripts/instabox-production-backup.sh; do
  if git show "FETCH_HEAD:$file" | cmp -s - "$BACKEND/$file"; then
    chmod 700 "$BACKEND/$file"
    continue
  fi
  if ! git diff --quiet -- "$file" || ! git diff --cached --quiet -- "$file"; then
    die "local edits to $file differ from the approved main-branch backup helper; review them before refreshing"
  fi
  tmp_file="$BACKUP_TMP_ROOT/$(basename "$file").new"
  git show "FETCH_HEAD:$file" > "$tmp_file"
  chmod 700 "$tmp_file"
  mv "$tmp_file" "$BACKEND/$file"
done

# Never infer that a failed repository read means the repository is new.
# A transient network/authentication error must not trigger initialization.
# Initialize the repository explicitly once, using the same endpoint and
# credentials, then require its config to be readable before any backup.
restic cat config >/dev/null || die "Restic repository is not initialized or cannot be read; verify credentials/network, and initialize it explicitly only if this bucket prefix is genuinely empty"

./scripts/self-hosted-backup.sh backup

if [[ "${RUN_PRUNE:-false}" == "true" ]]; then
  ./scripts/self-hosted-backup.sh prune
fi

if [[ "${RUN_RESTORE_DRILL:-false}" == "true" ]]; then
  ./scripts/self-hosted-restore-drill.sh
fi

printf 'InstaBox production backup workflow completed successfully.\n'
