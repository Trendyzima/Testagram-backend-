#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

# Restore and validate a backup in an isolated disposable PostgreSQL container.
# This never connects to the live application database and publishes no ports.

die() { printf 'restore drill error: %s\n' "$*" >&2; exit 1; }
command -v docker >/dev/null || die "docker is required"
command -v restic >/dev/null || die "restic is required"
command -v openssl >/dev/null || die "openssl is required"
command -v jq >/dev/null || die "jq is required for restore capacity checks"
[[ -n "${RESTIC_REPOSITORY:-}" ]] || die "RESTIC_REPOSITORY is required"
if [[ -n "${RESTIC_PASSWORD_FILE:-}" ]]; then
  [[ -f "$RESTIC_PASSWORD_FILE" && -r "$RESTIC_PASSWORD_FILE" ]] || die "RESTIC_PASSWORD_FILE must point to a readable password file"
  password_mode="$(stat -c '%a' "$RESTIC_PASSWORD_FILE" 2>/dev/null || stat -f '%Lp' "$RESTIC_PASSWORD_FILE" 2>/dev/null)" || die "cannot inspect restic password file permissions"
  [[ "$password_mode" == "600" ]] || die "password file must have mode 600"
elif [[ -z "${RESTIC_PASSWORD:-}" ]]; then
  die "set RESTIC_PASSWORD_FILE or RESTIC_PASSWORD"
fi

DOCKER=(docker)
if ! docker info >/dev/null 2>&1; then
  command -v sudo >/dev/null || die "Docker is not accessible and sudo is unavailable"
  sudo -n docker info >/dev/null 2>&1 || die "Docker is not accessible to this user or passwordless sudo"
  DOCKER=(sudo -n docker)
fi

export RESTIC_CACHE_DIR="${RESTIC_CACHE_DIR:-${HOME}/.cache/restic-testagram}"
restore_parent="${RESTORE_STAGE_DIR:-${TMPDIR:-/tmp}}"
mkdir -p "$restore_parent"
[[ -d "$restore_parent" && -w "$restore_parent" ]] || die "RESTORE_STAGE_DIR must be a writable directory"
restore_size_json="$(restic stats --host testagram-self-hosted latest --tag testagram --json)"
restore_bytes="$(jq -er '.total_size | select(type == "number" and . >= 0)' <<< "$restore_size_json")"
available_restore_bytes="$(df -PB1 "$restore_parent" | awk 'NR == 2 { print $4 }')"
[[ "$available_restore_bytes" =~ ^[0-9]+$ ]] || die "could not determine free restore staging space"
required_restore_bytes=$(( restore_bytes * 120 / 100 + 1073741824 ))
printf 'Restore estimate: %s bytes; staging free: %s bytes.\n' "$restore_bytes" "$available_restore_bytes"
(( available_restore_bytes >= required_restore_bytes )) || die "insufficient free staging space for a full isolated restore"

restore_dir="$(mktemp -d "${restore_parent%/}/testagram-restore-drill.XXXXXXXX")"
chmod 700 "$restore_dir"
suffix="${BASHPID}"
container_name="testagram-restore-drill-$suffix"
volume_name="testagram-restore-drill-$suffix"
cleanup() {
  "${DOCKER[@]}" rm -f "$container_name" >/dev/null 2>&1 || true
  "${DOCKER[@]}" volume rm "$volume_name" >/dev/null 2>&1 || true
  rm -rf -- "$restore_dir"
}
trap cleanup EXIT INT TERM

printf 'Restoring the latest tagged backup to private temporary storage...\n'
restic restore --host testagram-self-hosted latest --tag testagram --target "$restore_dir"
manifest_path="$(find "$restore_dir" -type f -name manifest.txt -print -quit)"
[[ -n "$manifest_path" ]] || die "snapshot is missing its backup manifest"
backup_root="$(dirname "$manifest_path")"
database_names_path="$backup_root/postgres-databases.txt"
[[ -s "$database_names_path" ]] || die "snapshot is missing its PostgreSQL database inventory"
manifest_value() {
  local key="$1"
  # shellcheck disable=SC2016
  awk -F= -v key="$key" '$1 == key { sub(/^[^=]*=/, ""); print; exit }' "$manifest_path"
}
restore_db_user="$(manifest_value postgres_user)"
restore_primary_db="$(manifest_value postgres_primary_database)"
postgres_source_bytes="$(manifest_value postgres_source_bytes)"
manifest_database_count="$(manifest_value postgres_database_count)"
[[ "$restore_db_user" =~ ^[A-Za-z0-9_]+$ && "$restore_primary_db" =~ ^[A-Za-z0-9_]+$ ]] || die "backup manifest has invalid PostgreSQL user/database names"
[[ "$postgres_source_bytes" =~ ^[0-9]+$ ]] || die "backup manifest has no valid PostgreSQL source size"
grep -Fxq "$restore_primary_db" "$database_names_path" || die "primary PostgreSQL database is absent from the backup inventory"

database_count=0
dump_bytes=0
while IFS= read -r database_name; do
  [[ "$database_name" =~ ^[A-Za-z0-9_]+$ ]] || die "backup contains an unsupported PostgreSQL database name"
  dump_path="$backup_root/postgres-$database_name.dump"
  [[ -s "$dump_path" && -s "$dump_path.list" ]] || die "snapshot is missing a validated dump for PostgreSQL database $database_name"
  dump_bytes=$(( dump_bytes + $(stat -c '%s' "$dump_path") ))
  database_count=$(( database_count + 1 ))
done < "$database_names_path"
(( database_count > 0 )) || die "snapshot contains no PostgreSQL databases"
[[ "$manifest_database_count" == "$database_count" ]] || die "backup manifest database count does not match the restored inventory"

docker_root="$("${DOCKER[@]}" info --format '{{.DockerRootDir}}')"
available_docker_bytes="$(df -PB1 "$docker_root" | awk 'NR == 2 { print $4 }')"
required_docker_bytes=$(( postgres_source_bytes * 120 / 100 + 1073741824 ))
[[ "$available_docker_bytes" =~ ^[0-9]+$ ]] || die "could not determine free Docker storage"
(( available_docker_bytes >= required_docker_bytes )) || die "insufficient Docker storage for the full isolated PostgreSQL cluster restore; refusing to create the container"

db_password="$(openssl rand -hex 24)"
"${DOCKER[@]}" volume create "$volume_name" >/dev/null
"${DOCKER[@]}" run --detach --name "$container_name" --network none \
  --env "POSTGRES_USER=$restore_db_user" \
  --env "POSTGRES_PASSWORD=$db_password" \
  --env "POSTGRES_DB=$restore_primary_db" \
  --volume "$volume_name:/var/lib/postgresql/data" \
  postgres:17-alpine >/dev/null

ready=0
for _ in $(seq 1 60); do
  if "${DOCKER[@]}" exec "$container_name" pg_isready -U "$restore_db_user" -d "$restore_primary_db" >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 1
done
[[ "$ready" == "1" ]] || die "isolated PostgreSQL did not become ready within 60 seconds"

printf 'Restoring %s PostgreSQL databases into isolated PostgreSQL...\n' "$database_count"
while IFS= read -r database_name; do
  dump_path="$backup_root/postgres-$database_name.dump"
  if [[ "$database_name" != "$restore_primary_db" && "$database_name" != "postgres" ]]; then
    "${DOCKER[@]}" exec "$container_name" createdb -U "$restore_db_user" "$database_name"
  fi
  "${DOCKER[@]}" exec -i "$container_name" pg_restore --exit-on-error --no-owner --no-acl \
    --dbname="postgresql://${restore_db_user}:${db_password}@127.0.0.1:5432/${database_name}?sslmode=disable" < "$dump_path"
  database_exists="$("${DOCKER[@]}" exec "$container_name" psql -U "$restore_db_user" -d postgres -Atqc \
    "SELECT 1 FROM pg_database WHERE datname = '$database_name'")"
  [[ "$database_exists" == "1" ]] || die "restored PostgreSQL database $database_name is missing"
done < "$database_names_path"

table_count="$("${DOCKER[@]}" exec "$container_name" psql -U "$restore_db_user" -d "$restore_primary_db" -Atqc \
  "select count(*) from pg_tables where schemaname not in ('pg_catalog','information_schema')")"
[[ "$table_count" =~ ^[0-9]+$ && "$table_count" -gt 0 ]] || die "restored primary database contains no application tables"

for bucket_dir in minio-private minio-public minio-identity-evidence minio-metadata; do
  restored_bucket="$(find "$restore_dir" -type d -name "$bucket_dir" -print -quit)"
  [[ -n "$restored_bucket" ]] || die "snapshot is missing the $bucket_dir backup directory"
done

if grep -qx 'data_centre_video_media_included=true' "$manifest_path"; then
  restored_video="$(find "$restore_dir" -type d -name data-centre-video-media -print -quit)"
  [[ -n "$restored_video" ]] || die "manifest requires Data Centre video media but the snapshot is missing it"
fi
if grep -qx 'data_centre_node_storage_included=true' "$manifest_path"; then
  restored_nodes="$(find "$restore_dir" -type d -name data-centre-node-storage -print -quit)"
  [[ -n "$restored_nodes" ]] || die "manifest requires Data Centre node storage but the snapshot is missing it"
fi

private_count="$(find "$restore_dir" -type f -path '*/minio-private/*' | wc -l | tr -d ' ')"
public_count="$(find "$restore_dir" -type f -path '*/minio-public/*' | wc -l | tr -d ' ')"
identity_count="$(find "$restore_dir" -type f -path '*/minio-identity-evidence/*' | wc -l | tr -d ' ')"
metadata_count="$(find "$restore_dir" -type f -path '*/minio-metadata/*' | wc -l | tr -d ' ')"
video_count="$(find "$restore_dir" -type f -path '*/data-centre-video-media/*' | wc -l | tr -d ' ')"
node_count="$(find "$restore_dir" -type f -path '*/data-centre-node-storage/*' | wc -l | tr -d ' ')"
printf 'Restore drill passed: PostgreSQL_databases=%s primary_application_tables=%s private_media_files=%s public_media_files=%s identity_evidence_files=%s metadata_files=%s data_centre_video_files=%s node_storage_files=%s (validated_dump_bytes=%s)\n' "$database_count" "$table_count" "$private_count" "$public_count" "$identity_count" "$metadata_count" "$video_count" "$node_count" "$dump_bytes"
printf 'This validates a database restore in isolation; it does not switch traffic or restore a live MinIO service.\n'
