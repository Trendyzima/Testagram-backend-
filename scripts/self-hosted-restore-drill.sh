#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

# Restore and validate a backup in an isolated disposable PostgreSQL container.
# This never connects to the live application database and publishes no ports.

die() { printf 'restore drill error: %s\n' "$*" >&2; exit 1; }
command -v docker >/dev/null || die "docker is required"
command -v restic >/dev/null || die "restic is required"
command -v openssl >/dev/null || die "openssl is required"
[[ -n "${RESTIC_REPOSITORY:-}" ]] || die "RESTIC_REPOSITORY is required"
[[ -n "${RESTIC_PASSWORD_FILE:-}" && -f "${RESTIC_PASSWORD_FILE}" ]] || die "RESTIC_PASSWORD_FILE must point to a readable password file"
[[ -r "$RESTIC_PASSWORD_FILE" ]] || die "RESTIC_PASSWORD_FILE is not readable"
password_mode="$(stat -c '%a' "$RESTIC_PASSWORD_FILE" 2>/dev/null || stat -f '%Lp' "$RESTIC_PASSWORD_FILE" 2>/dev/null)" || die "cannot inspect restic password file permissions"
[[ "$password_mode" == "600" ]] || die "password file must have mode 600"

export RESTIC_CACHE_DIR="${RESTIC_CACHE_DIR:-${HOME}/.cache/restic-testagram}"
restore_parent="${RESTORE_STAGE_DIR:-${TMPDIR:-/tmp}}"
mkdir -p "$restore_parent"
[[ -d "$restore_parent" && -w "$restore_parent" ]] || die "RESTORE_STAGE_DIR must be a writable directory"
restore_dir="$(mktemp -d "${restore_parent%/}/testagram-restore-drill.XXXXXXXX")"
chmod 700 "$restore_dir"
suffix="$$"
container_name="testagram-restore-drill-$suffix"
volume_name="testagram-restore-drill-$suffix"
cleanup() {
  docker rm -f "$container_name" >/dev/null 2>&1 || true
  docker volume rm "$volume_name" >/dev/null 2>&1 || true
  rm -rf -- "$restore_dir"
}
trap cleanup EXIT INT TERM

printf 'Restoring the latest tagged backup to private temporary storage...\n'
restic restore latest --tag testagram --target "$restore_dir"
dump_path="$(find "$restore_dir" -type f -name postgres.dump -print -quit)"
[[ -n "$dump_path" && -s "$dump_path" ]] || die "snapshot does not contain a non-empty PostgreSQL dump"

db_password="$(openssl rand -hex 24)"
docker volume create "$volume_name" >/dev/null
docker run --detach --name "$container_name" --network none \
  --env POSTGRES_USER=testagram \
  --env POSTGRES_PASSWORD="$db_password" \
  --env POSTGRES_DB=testagram \
  --volume "$volume_name:/var/lib/postgresql/data" \
  --volume "$dump_path:/tmp/postgres.dump:ro" \
  postgres:17-alpine >/dev/null

ready=0
for _ in $(seq 1 60); do
  if docker exec "$container_name" pg_isready -U testagram -d testagram >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 1
done
[[ "$ready" == "1" ]] || die "isolated PostgreSQL did not become ready within 60 seconds"

printf 'Restoring the database dump into isolated PostgreSQL...\n'
docker exec "$container_name" pg_restore --exit-on-error --no-owner --no-acl \
  --dbname="postgresql://testagram:${db_password}@127.0.0.1:5432/testagram?sslmode=disable" \
  /tmp/postgres.dump

table_count="$(docker exec "$container_name" psql -U testagram -d testagram -Atqc \
  "select count(*) from pg_tables where schemaname not in ('pg_catalog','information_schema')")"
[[ "$table_count" =~ ^[0-9]+$ && "$table_count" -gt 0 ]] || die "restored database contains no application tables"

private_count="$(find "$restore_dir" -type f -path '*/minio-private/*' | wc -l | tr -d ' ')"
public_count="$(find "$restore_dir" -type f -path '*/minio-public/*' | wc -l | tr -d ' ')"
printf 'Restore drill passed: application_tables=%s private_media_files=%s public_media_files=%s\n' "$table_count" "$private_count" "$public_count"
printf 'This validates a database restore in isolation; it does not switch traffic or restore a live MinIO service.\n'
