#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

# Run from the repository root or any directory. Use an off-host restic repository in production.
# Required environment: RESTIC_REPOSITORY and either RESTIC_PASSWORD_FILE or RESTIC_PASSWORD.
# Optional: RESTIC_CACHE_DIR, BACKUP_STAGE_DIR, RESTIC_KEEP_DAILY/WEEKLY/MONTHLY.

usage() {
  cat <<'USAGE'
Usage: ./scripts/self-hosted-backup.sh <backup|check|prune>

  backup  Export PostgreSQL, all four MinIO buckets, and detected local Data
          Centre video/node storage into the encrypted restic repository.
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
command -v restic >/dev/null || die "restic is required"
[[ -n "${RESTIC_REPOSITORY:-}" ]] || die "RESTIC_REPOSITORY is required"
if [[ -n "${RESTIC_PASSWORD_FILE:-}" ]]; then
  [[ -f "$RESTIC_PASSWORD_FILE" && -r "$RESTIC_PASSWORD_FILE" ]] || die "RESTIC_PASSWORD_FILE must point to a readable password file"
  [[ "$(stat -c '%a' "$RESTIC_PASSWORD_FILE" 2>/dev/null || stat -f '%Lp' "$RESTIC_PASSWORD_FILE" 2>/dev/null)" == "600" ]] || die "password file must have mode 600"
elif [[ -z "${RESTIC_PASSWORD:-}" ]]; then
  die "set RESTIC_PASSWORD_FILE or RESTIC_PASSWORD"
fi

export RESTIC_CACHE_DIR="${RESTIC_CACHE_DIR:-${HOME}/.cache/restic-testagram}"

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
compose_dir="$(cd -- "$script_dir/../deploy/self-hosted" && pwd)"
repo_root="$(cd -- "$compose_dir/../.." && pwd)"
media_backup_helper="$repo_root/scripts/self_hosted_media_backup.py"
cd "$compose_dir"

case "${1:-}" in
  backup)
command -v docker >/dev/null || die "docker is required"
command -v jq >/dev/null || die "jq is required for the backup capacity preflight"
command -v realpath >/dev/null || die "realpath is required to resolve Data Centre storage mounts"
command -v python3 >/dev/null || die "python3 is required to validate Data Centre database routing"

# InstaBox's standard template may expose Docker through sudo rather than the
# user's supplementary groups. Prefer unprivileged Docker, then use passwordless
# sudo only when needed; never change Docker group membership during a backup.
DOCKER=(docker)
if ! docker info >/dev/null 2>&1; then
  command -v sudo >/dev/null || die "Docker is not accessible and sudo is unavailable"
  sudo -n docker info >/dev/null 2>&1 || die "Docker is not accessible to this user or passwordless sudo"
  DOCKER=(sudo -n docker)
fi

stage=""
provider_inventory_dir=""
media_container_id=""
media_external_enabled=false
cleanup() {
  [[ -z "$stage" || ! -d "$stage" ]] || rm -rf -- "$stage"
  [[ -z "$provider_inventory_dir" || ! -d "$provider_inventory_dir" ]] || rm -rf -- "$provider_inventory_dir"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Back up the effective Data Centre B2/R2 object stores when that service is
# active. Backend's separate legacy three-provider media path remains fail-closed
# until its own provider buckets are added to this snapshot.
read_env_flag() {
  local key="$1"
  # shellcheck disable=SC2016
  awk -F= -v key="$key" '$1 == key {
    value = $2
    gsub(/^[[:space:]"]+|[[:space:]"]+$/, "", value)
  } END { print tolower(value) }' .env
}

read_env_value() {
  local key="$1"
  # shellcheck disable=SC2016
  awk -F= -v key="$key" '$1 == key {
    value = substr($0, index($0, "=") + 1)
    gsub(/^[[:space:]"]+|[[:space:]"]+$/, "", value)
  } END { print value }' .env
}

container_env() {
  local container="$1" key="$2"
  "${DOCKER[@]}" inspect "$container" | jq -er --arg prefix "$key=" \
    '[.[0].Config.Env[]? | select(startswith($prefix))] | last | ltrimstr($prefix)' 2>/dev/null
}
if [[ "$(read_env_flag MEDIA_REPLICATION_ENABLED)" == "true" || "$(read_env_flag MEDIA_REPLICATION_ENABLED)" == "1" ]]; then
  die "Backend MEDIA_REPLICATION_ENABLED is active; its separate provider buckets are not part of this Data Centre B2/R2 snapshot"
fi

# The production deployment intentionally sets MINIO_PUBLIC_ENDPOINT to the
# bundled Compose service address. Allow that exact internal origin; reject any
# override that would move the API's effective object store off the backed-up
# MinIO instance.
validate_local_minio_endpoint() {
  local endpoint="$1"
  [[ -z "$endpoint" || "$endpoint" == "http://object-storage:9000" ]]
}

for endpoint_var in MEDIA_S3_ENDPOINT R2_S3_ENDPOINT; do
  if [[ -n "$(read_env_value "$endpoint_var")" ]]; then
    die "$endpoint_var is configured; verify and back up that effective S3 origin before relying on this local-MinIO backup"
  fi
done

if ! validate_local_minio_endpoint "$(read_env_value MINIO_PUBLIC_ENDPOINT)"; then
  die "MINIO_PUBLIC_ENDPOINT overrides the bundled http://object-storage:9000 origin; verify and back up that effective S3 origin before relying on this local-MinIO backup"
fi


parse_postgres_identity() {
  python3 - "$1" <<'PY'
import re
import sys
from urllib.parse import unquote, urlparse
try:
    parsed = urlparse(sys.argv[1])
    host = (parsed.hostname or "").lower()
    port = parsed.port or 5432
except ValueError:
    sys.exit(1)
user = unquote(parsed.username or "")
database = unquote(parsed.path.lstrip("/"))
if parsed.scheme not in {"postgres", "postgresql"} or host not in {"postgres", "federation-db"} or port != 5432 or not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]{0,62}", user) or not re.fullmatch(r"[A-Za-z0-9_]{1,63}", database):
    sys.exit(1)
print(user + "|" + database)
PY
}

federation_database_url="$(read_env_value FEDERATION_DATABASE_URL)"
federation_api_ids="$("${DOCKER[@]}" ps -a --filter label=com.docker.compose.project=testagram-data-centre --filter label=com.docker.compose.service=federation-api --format '{{.ID}}')"
federation_database_name=""
federation_database_user=""
if [[ -n "$federation_api_ids" ]]; then
  [[ "$(wc -l <<< "$federation_api_ids" | tr -d ' ')" == "1" ]] || die "multiple Data Centre federation-api containers found; verify their database targets"
  federation_api_id="$(head -n 1 <<< "$federation_api_ids")"
  federation_running="$("${DOCKER[@]}" inspect "$federation_api_id" | jq -er '.[0].State.Running')"
  [[ "$federation_running" == "true" ]] || die "Data Centre federation-api exists but is not running"
  federation_database_url="$(container_env "$federation_api_id" FEDERATION_DATABASE_URL)" || die "Data Centre federation-api has no readable FEDERATION_DATABASE_URL"
fi
if [[ -n "$federation_database_url" ]]; then
  federation_identity="$(parse_postgres_identity "$federation_database_url")" || die "Federation database is not on the backed-up PostgreSQL service"
  IFS='|' read -r federation_database_user federation_database_name <<< "$federation_identity"
fi

data_centre_media_ids="$("${DOCKER[@]}" ps -a --filter label=com.docker.compose.project=testagram-data-centre --filter label=com.docker.compose.service=media --format '{{.ID}}')"
media_database_url=""
media_database_name=""
media_database_user=""
media_b2_endpoint=""
media_b2_region=""
media_b2_bucket=""
media_b2_access=""
media_b2_secret=""
media_r2_endpoint=""
media_r2_region=""
media_r2_bucket=""
media_r2_access=""
media_r2_secret=""
media_r2_metadata_bucket=""
media_vps_metadata_bucket=""
if [[ -n "$data_centre_media_ids" ]]; then
  [[ "$(wc -l <<< "$data_centre_media_ids" | tr -d ' ')" == "1" ]] || die "multiple Data Centre media containers found"
  media_container_id="$(head -n 1 <<< "$data_centre_media_ids")"
  media_running="$("${DOCKER[@]}" inspect "$media_container_id" | jq -er '.[0].State.Running')"
  [[ "$media_running" == "true" ]] || die "Data Centre media container exists but is not running; refusing an incomplete media backup"
  [[ "$(read_env_flag FRESH_MEDIA_ENABLED)" == "true" || "$(read_env_flag FRESH_MEDIA_ENABLED)" == "1" ]] || die "Data Centre media container is running while FRESH_MEDIA_ENABLED is not true"
  [[ "$(read_env_value DATA_CENTRE_MEDIA_API_URL)" == "http://media:8096" ]] || die "Backend is not configured for the active private Data Centre media service"
  media_database_url="$(container_env "$media_container_id" MEDIA_DATABASE_URL)" || die "Data Centre media container has no readable MEDIA_DATABASE_URL"
  media_b2_endpoint="$(container_env "$media_container_id" B2_S3_ENDPOINT)" || true
  media_b2_region="$(container_env "$media_container_id" B2_S3_REGION)" || true
  media_b2_bucket="$(container_env "$media_container_id" B2_S3_BUCKET)" || true
  media_b2_access="$(container_env "$media_container_id" B2_S3_ACCESS_KEY_ID)" || true
  media_b2_secret="$(container_env "$media_container_id" B2_S3_SECRET_ACCESS_KEY)" || true
  media_r2_endpoint="$(container_env "$media_container_id" CLOUDFLARE_R2_ENDPOINT)" || true
  media_r2_region="$(container_env "$media_container_id" CLOUDFLARE_R2_REGION)" || true
  media_r2_bucket="$(container_env "$media_container_id" CLOUDFLARE_R2_BUCKET)" || true
  media_r2_access="$(container_env "$media_container_id" CLOUDFLARE_R2_ACCESS_KEY_ID)" || true
  media_r2_secret="$(container_env "$media_container_id" CLOUDFLARE_R2_SECRET_ACCESS_KEY)" || true
  media_r2_metadata_bucket="$(container_env "$media_container_id" CLOUDFLARE_R2_METADATA_BUCKET)" || true
  media_vps_metadata_bucket="$(container_env "$media_container_id" VPS_MINIO_METADATA_BUCKET)" || true
  media_identity="$(parse_postgres_identity "$media_database_url")" || die "Data Centre media database is not on the backed-up PostgreSQL service"
  IFS='|' read -r media_database_user media_database_name <<< "$media_identity"
  for value in "$media_b2_endpoint" "$media_b2_region" "$media_b2_bucket" "$media_b2_access" "$media_b2_secret" "$media_r2_endpoint" "$media_r2_region" "$media_r2_bucket" "$media_r2_access" "$media_r2_secret" "$media_r2_metadata_bucket" "$media_vps_metadata_bucket"; do
    [[ -n "$value" ]] || die "Data Centre media provider configuration is incomplete; refusing to omit external objects"
  done
  media_external_enabled=true
elif [[ "$(read_env_flag FRESH_MEDIA_ENABLED)" == "true" || "$(read_env_flag FRESH_MEDIA_ENABLED)" == "1" || -n "$(read_env_value DATA_CENTRE_MEDIA_API_URL)" ]]; then
  die "Data Centre media is configured but its runtime container is missing; refusing to omit external objects"
fi

if [[ "$media_external_enabled" == "true" ]]; then
  provider_mc() {
    local mount_dir="$1" mount_point="$2" script="$3"
    "${DOCKER[@]}" compose run --rm --no-deps --network testagram-egress \
      -v "$mount_dir:$mount_point" \
      -e B2_S3_ENDPOINT="$media_b2_endpoint" -e B2_S3_REGION="$media_b2_region" \
      -e B2_S3_BUCKET="$media_b2_bucket" -e B2_S3_ACCESS_KEY_ID="$media_b2_access" \
      -e B2_S3_SECRET_ACCESS_KEY="$media_b2_secret" \
      -e CLOUDFLARE_R2_ENDPOINT="$media_r2_endpoint" -e CLOUDFLARE_R2_REGION="$media_r2_region" \
      -e CLOUDFLARE_R2_BUCKET="$media_r2_bucket" -e CLOUDFLARE_R2_ACCESS_KEY_ID="$media_r2_access" \
      -e CLOUDFLARE_R2_SECRET_ACCESS_KEY="$media_r2_secret" \
      -e CLOUDFLARE_R2_METADATA_BUCKET="$media_r2_metadata_bucket" \
      --entrypoint /bin/sh object-init -ec "$script"
  }
fi

federation_db_ids="$("${DOCKER[@]}" ps -a --filter label=com.docker.compose.service=federation-db --format '{{.ID}}')"
[[ -z "$federation_db_ids" ]] || die "a separate federation-db container exists; back it up and restore-test it before relying on this backup"

video_media_source=""
for service_name in video video-worker; do
  service_ids="$("${DOCKER[@]}" ps -a --filter label=com.docker.compose.project=testagram-data-centre --filter "label=com.docker.compose.service=$service_name" --format '{{.ID}}')"
  if [[ -n "$service_ids" ]]; then
    [[ "$(wc -l <<< "$service_ids" | tr -d ' ')" == "1" ]] || die "multiple Data Centre $service_name containers found; resolve the storage mounts before backup"
    service_id="$(head -n 1 <<< "$service_ids")"
    current_source="$("${DOCKER[@]}" inspect "$service_id" | jq -er '.[0].Mounts[] | select(.Destination == "/media" and .Type == "bind") | .Source' 2>/dev/null || true)"
    [[ -n "$current_source" && -d "$current_source" ]] || die "Data Centre $service_name does not expose a readable bind mount at /media; refusing to omit its files"
    current_source="$(realpath "$current_source")"
    if [[ -n "$video_media_source" && "$video_media_source" != "$current_source" ]]; then
      die "Data Centre video and video-worker use different media sources; reconcile them before backup"
    fi
    video_media_source="$current_source"
  fi
done

storage_root="$(read_env_value TESTAGRAM_STORAGE_ROOT)"
[[ -n "$storage_root" ]] || storage_root="/var/lib/testagram/storage"
if [[ -z "$video_media_source" && -d "$storage_root/media" ]]; then
  video_media_source="$(realpath "$storage_root/media")"
fi
node_storage_source=""
storage_root_for_nodes="$storage_root"
if [[ ! -d "$storage_root_for_nodes/nodes" && -n "$video_media_source" ]]; then
  storage_root_for_nodes="$(dirname "$video_media_source")"
fi
if [[ -d "$storage_root_for_nodes/nodes" ]]; then
  node_storage_source="$(realpath "$storage_root_for_nodes/nodes")"
fi

"${DOCKER[@]}" compose config --quiet
    postgres_user="$("${DOCKER[@]}" compose exec -T postgres printenv POSTGRES_USER | tr -d '\r')"
    postgres_primary_db="$("${DOCKER[@]}" compose exec -T postgres printenv POSTGRES_DB | tr -d '\r')"
    [[ "$postgres_user" =~ ^[A-Za-z0-9_]+$ && "$postgres_primary_db" =~ ^[A-Za-z0-9_]+$ ]] || die "PostgreSQL user/database names are missing or unsupported"
    allowed_postgres_roles="'postgres', '$postgres_user'"
    [[ -z "$federation_database_user" ]] || allowed_postgres_roles+=", '$federation_database_user'"
    [[ -z "$media_database_user" ]] || allowed_postgres_roles+=", '$media_database_user'"
    extra_postgres_roles="$("${DOCKER[@]}" compose exec -T postgres psql -U "$postgres_user" -d "$postgres_primary_db" -Atqc "SELECT rolname FROM pg_roles WHERE rolname !~ '^pg_' AND rolname NOT IN ($allowed_postgres_roles) ORDER BY rolname")"
    [[ -z "$extra_postgres_roles" ]] || die "unexpected PostgreSQL roles detected ($extra_postgres_roles); add them to the coordinated service database backup contract"
    extra_tablespaces="$("${DOCKER[@]}" compose exec -T postgres psql -U "$postgres_user" -d "$postgres_primary_db" -Atqc "SELECT spcname FROM pg_tablespace WHERE spcname NOT IN ('pg_default', 'pg_global') ORDER BY spcname")"
    [[ -z "$extra_tablespaces" ]] || die "user-defined PostgreSQL tablespaces detected ($extra_tablespaces); back up their external paths before relying on this workflow"
    postgres_database_names="$("${DOCKER[@]}" compose exec -T postgres psql -U "$postgres_user" -d "$postgres_primary_db" -Atqc "SELECT datname FROM pg_database WHERE datallowconn AND NOT datistemplate ORDER BY datname")"
    [[ -n "$postgres_database_names" ]] || die "PostgreSQL returned no connectable databases"
    grep -Fxq "$postgres_primary_db" <<< "$postgres_database_names" || die "primary PostgreSQL database is absent from the database inventory"
    if [[ -n "$federation_database_name" ]]; then
      grep -Fxq "$federation_database_name" <<< "$postgres_database_names" || die "Federation API points to a database that is not present in the backed-up PostgreSQL cluster"
    fi
    if [[ -n "$media_database_name" ]]; then
      grep -Fxq "$media_database_name" <<< "$postgres_database_names" || die "Data Centre media points to a database that is not present in the backed-up PostgreSQL cluster"
    fi
    while IFS= read -r database_name; do
      [[ "$database_name" =~ ^[A-Za-z0-9_]+$ ]] || die "unsupported PostgreSQL database name discovered; refusing unsafe dump path"
    done <<< "$postgres_database_names"
    stage_parent="${BACKUP_STAGE_DIR:-${TMPDIR:-/tmp}}"
    mkdir -p "$stage_parent"
    stage_parent_real="$(realpath "$stage_parent")"
    if [[ "$media_external_enabled" == "true" ]]; then
      provider_inventory_dir="$(mktemp -d "${stage_parent_real%/}/testagram-provider-inventory.XXXXXXXX")"
      chmod 700 "$provider_inventory_dir"
      provider_mc "$provider_inventory_dir" /inventory '
        mc alias set b2 "$B2_S3_ENDPOINT" "$B2_S3_ACCESS_KEY_ID" "$B2_S3_SECRET_ACCESS_KEY" --api S3v4 --path auto --region "$B2_S3_REGION" >/dev/null
        mc alias set r2 "$CLOUDFLARE_R2_ENDPOINT" "$CLOUDFLARE_R2_ACCESS_KEY_ID" "$CLOUDFLARE_R2_SECRET_ACCESS_KEY" --api S3v4 --path auto --region "$CLOUDFLARE_R2_REGION" >/dev/null
        mc ls --recursive --json "b2/$B2_S3_BUCKET" > /inventory/b2-media.jsonl
        mc ls --recursive --json "r2/$CLOUDFLARE_R2_BUCKET" > /inventory/r2-media.jsonl
        mc ls --recursive --json "r2/$CLOUDFLARE_R2_METADATA_BUCKET" > /inventory/r2-metadata.jsonl
      '
      b2_inventory_stats="$(python3 "$media_backup_helper" stats --inventory "$provider_inventory_dir/b2-media.jsonl" --bucket "$media_b2_bucket")"
      r2_inventory_stats="$(python3 "$media_backup_helper" stats --inventory "$provider_inventory_dir/r2-media.jsonl" --bucket "$media_r2_bucket")"
      r2_metadata_inventory_stats="$(python3 "$media_backup_helper" stats --inventory "$provider_inventory_dir/r2-metadata.jsonl" --bucket "$media_r2_metadata_bucket")"
      b2_media_count="$(jq -er '.count' <<< "$b2_inventory_stats")"
      b2_media_bytes="$(jq -er '.bytes' <<< "$b2_inventory_stats")"
      r2_media_count="$(jq -er '.count' <<< "$r2_inventory_stats")"
      r2_media_bytes="$(jq -er '.bytes' <<< "$r2_inventory_stats")"
      r2_metadata_count="$(jq -er '.count' <<< "$r2_metadata_inventory_stats")"
      r2_metadata_bytes="$(jq -er '.bytes' <<< "$r2_metadata_inventory_stats")"
      external_media_bytes=$((b2_media_bytes + r2_media_bytes + r2_metadata_bytes))
    else
      external_media_bytes=0
      b2_media_count=0; b2_media_bytes=0
      r2_media_count=0; r2_media_bytes=0
      r2_metadata_count=0; r2_metadata_bytes=0
    fi
    for source_path in "$video_media_source" "$node_storage_source"; do
      [[ -n "$source_path" ]] || continue
      [[ "$source_path" != "/" ]] || die "refusing to back up the entire filesystem as Data Centre media"
      case "$stage_parent_real/" in
        "$source_path/"*) die "backup staging directory is inside a Data Centre source directory" ;;
      esac
      case "$source_path/" in
        "$stage_parent_real/"*) die "Data Centre source directory is inside the backup staging directory" ;;
      esac
    done

    printf 'Estimating PostgreSQL, MinIO and detected Data Centre files before staging...\n'
    # shellcheck disable=SC2016
    bucket_size_report="$("${DOCKER[@]}" compose run --rm --no-deps \
      --entrypoint /bin/sh object-init -ec '
        mc alias set local http://object-storage:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null
        mc stat "local/$MINIO_BUCKET" --json
        mc stat "local/$PUBLIC_MEDIA_BUCKET" --json
        mc stat "local/$IDENTITY_EVIDENCE_PRIVATE_BUCKET" --json
        mc stat "local/$MINIO_METADATA_BUCKET" --json
      ')"
    minio_bytes="$(jq -s -er 'if length == 4 and all(.[]; (.Usage.totalSize | type) == "number") then map(.Usage.totalSize) | add else error("MinIO did not report sizes for all four buckets") end' <<< "$bucket_size_report")"
    postgres_bytes="$("${DOCKER[@]}" compose exec -T postgres psql -U "$postgres_user" -d "$postgres_primary_db" -Atqc "SELECT COALESCE(SUM(pg_database_size(datname)),0) FROM pg_database WHERE datallowconn AND NOT datistemplate")"
    video_media_bytes=0
    node_storage_bytes=0
    if [[ -n "$video_media_source" ]]; then
      video_media_bytes="$(du -sb "$video_media_source" | awk '{print $1}')"
    fi
    if [[ -n "$node_storage_source" ]]; then
      node_storage_bytes="$(du -sb "$node_storage_source" | awk '{print $1}')"
    fi
    [[ "$minio_bytes" =~ ^[0-9]+$ && "$postgres_bytes" =~ ^[0-9]+$ && "$video_media_bytes" =~ ^[0-9]+$ && "$node_storage_bytes" =~ ^[0-9]+$ && "$external_media_bytes" =~ ^[0-9]+$ ]] || die "could not determine database, MinIO, Data Centre or external media storage sizes"
    required_stage_bytes=$(( (minio_bytes + postgres_bytes + video_media_bytes + node_storage_bytes + external_media_bytes) * 120 / 100 + 1073741824 ))
    available_stage_bytes="$(df -PB1 "$stage_parent" | awk 'NR == 2 { print $4 }')"
    docker_root="$("${DOCKER[@]}" info --format '{{.DockerRootDir}}')"
    available_docker_bytes="$(df -PB1 "$docker_root" | awk 'NR == 2 { print $4 }')"
    [[ "$available_stage_bytes" =~ ^[0-9]+$ && "$available_docker_bytes" =~ ^[0-9]+$ ]] || die "could not determine free disk space"
    printf 'Estimated backup source: PostgreSQL=%s bytes, MinIO=%s bytes, Data Centre video=%s bytes, node storage=%s bytes, external media=%s bytes; staging free=%s bytes.\n' \
      "$postgres_bytes" "$minio_bytes" "$video_media_bytes" "$node_storage_bytes" "$external_media_bytes" "$available_stage_bytes"
    (( available_stage_bytes >= required_stage_bytes )) || die "insufficient staging space; need at least ${required_stage_bytes} bytes free for a safe full copy"
    (( available_docker_bytes >= 1073741824 )) || die "less than 1 GiB remains in Docker storage; refusing to risk production containers"

    stage=$(mktemp -d "${stage_parent%/}/testagram-backup.XXXXXXXX")
    chmod 700 "$stage"
    if [[ "$media_external_enabled" == "true" ]]; then
      cp "$provider_inventory_dir/b2-media.jsonl" "$stage/b2-media-inventory.jsonl"
      cp "$provider_inventory_dir/r2-media.jsonl" "$stage/r2-media-inventory.jsonl"
      cp "$provider_inventory_dir/r2-metadata.jsonl" "$stage/r2-metadata-inventory.jsonl"
    fi

    printf '%s\n' "$postgres_database_names" > "$stage/postgres-databases.txt"
    {
      [[ -z "$federation_database_name" ]] || printf '%s|%s\n' "$federation_database_name" "$federation_database_user"
      [[ -z "$media_database_name" ]] || printf '%s|%s\n' "$media_database_name" "$media_database_user"
    } > "$stage/service-db-roles.txt"
    printf 'Exporting %s PostgreSQL databases...\n' "$(wc -l < "$stage/postgres-databases.txt" | tr -d ' ')"
    capture_media_snapshot() {
      local prefix="$1" pending_objects pending_jobs
      "${DOCKER[@]}" compose exec -T postgres psql -v ON_ERROR_STOP=1 -U "$postgres_user" -d "$media_database_name" -At -F 

    if [[ -n "$video_media_source" ]]; then
      printf 'Copying Data Centre video media into protected staging...\n'
      cp -a -- "$video_media_source" "$stage/data-centre-video-media"
    fi
    if [[ -n "$node_storage_source" ]]; then
      printf 'Copying Data Centre node storage into protected staging...\n'
      cp -a -- "$node_storage_source" "$stage/data-centre-node-storage"
    fi

    printf 'Mirroring all MinIO buckets to protected staging...\n'
    mkdir -p "$stage/minio-private" "$stage/minio-public" "$stage/minio-identity-evidence" "$stage/minio-metadata"
    chmod 700 "$stage/minio-private" "$stage/minio-public" "$stage/minio-identity-evidence" "$stage/minio-metadata"
    # shellcheck disable=SC2016
    "${DOCKER[@]}" compose run --rm --no-deps \
      -v "$stage:/backup" \
      --entrypoint /bin/sh object-init -ec '
        mc alias set local http://object-storage:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null
        mc mirror --preserve local/"$MINIO_BUCKET" /backup/minio-private >/dev/null
        mc mirror --preserve local/"$PUBLIC_MEDIA_BUCKET" /backup/minio-public >/dev/null
        mc mirror --preserve local/"$IDENTITY_EVIDENCE_PRIVATE_BUCKET" /backup/minio-identity-evidence >/dev/null
        mc mirror --preserve local/"$MINIO_METADATA_BUCKET" /backup/minio-metadata >/dev/null
      '
    printf '%s\n' \
      "Testagram self-hosted backup" \
      "created_utc=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      "postgres_user=$postgres_user" \
      "postgres_primary_database=$postgres_primary_db" \
      "postgres_database_count=$(wc -l < "$stage/postgres-databases.txt" | tr -d ' ')" \
      "postgres_source_bytes=$postgres_bytes" \
      "private_bucket=${MINIO_BUCKET:-testagram-private-media}" \
      "public_bucket=${PUBLIC_MEDIA_BUCKET:-testagram-public-media}" \
      "identity_evidence_bucket=${IDENTITY_EVIDENCE_PRIVATE_BUCKET:-testagram-identity-evidence-private}" \
      "metadata_bucket=${MINIO_METADATA_BUCKET:-testagram-media-metadata}" \
      "data_centre_video_media_included=$([[ -n "$video_media_source" ]] && echo true || echo false)" \
      "data_centre_video_media_source=$video_media_source" \
      "data_centre_video_media_bytes=$video_media_bytes" \
      "data_centre_node_storage_included=$([[ -n "$node_storage_source" ]] && echo true || echo false)" \
      "data_centre_node_storage_source=$node_storage_source" \
      "data_centre_node_storage_bytes=$node_storage_bytes" \
      "repository=${RESTIC_REPOSITORY}" > "$stage/manifest.txt"
    chmod 600 "$stage/manifest.txt"
    printf 'Writing encrypted restic snapshot...\n'
    restic backup --host testagram-self-hosted --tag testagram --tag automated "$stage"
    printf 'Verifying repository metadata and a data subset...\n'
    restic check --read-data-subset=1/20
    printf 'Backup complete. Temporary plaintext staging will now be removed.\n'
    ;;
  check)
    restic snapshots --host testagram-self-hosted --tag testagram
    restic check --read-data-subset=1/20
    ;;
  prune)
    restic forget --host testagram-self-hosted --tag testagram \
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
\\t' read -r federation_database_user federation_database_name <<< "$federation_identity"
fi
if [[ "$media_external_enabled" == "true" ]]; then
  media_identity="$(parse_postgres_identity "$media_database_url")" || die "Data Centre media database is not on the backed-up PostgreSQL service"
  IFS=
data_centre_media_ids="$("${DOCKER[@]}" ps -a --filter label=com.docker.compose.project=testagram-data-centre --filter label=com.docker.compose.service=media --format '{{.ID}}')"
media_database_url=""
media_database_name=""
media_database_user=""
media_b2_endpoint=""
media_b2_region=""
media_b2_bucket=""
media_b2_access=""
media_b2_secret=""
media_r2_endpoint=""
media_r2_region=""
media_r2_bucket=""
media_r2_access=""
media_r2_secret=""
media_r2_metadata_bucket=""
media_vps_metadata_bucket=""
if [[ -n "$data_centre_media_ids" ]]; then
  [[ "$(wc -l <<< "$data_centre_media_ids" | tr -d ' ')" == "1" ]] || die "multiple Data Centre media containers found"
  media_container_id="$(head -n 1 <<< "$data_centre_media_ids")"
  media_running="$("${DOCKER[@]}" inspect "$media_container_id" | jq -er '.[0].State.Running')"
  [[ "$media_running" == "true" ]] || die "Data Centre media container exists but is not running; refusing an incomplete media backup"
  [[ "$(read_env_flag FRESH_MEDIA_ENABLED)" == "true" || "$(read_env_flag FRESH_MEDIA_ENABLED)" == "1" ]] || die "Data Centre media container is running while FRESH_MEDIA_ENABLED is not true"
  [[ "$(read_env_value DATA_CENTRE_MEDIA_API_URL)" == "http://media:8096" ]] || die "Backend is not configured for the active private Data Centre media service"
  media_database_url="$(container_env "$media_container_id" MEDIA_DATABASE_URL)" || die "Data Centre media container has no readable MEDIA_DATABASE_URL"
  media_b2_endpoint="$(container_env "$media_container_id" B2_S3_ENDPOINT)" || true
  media_b2_region="$(container_env "$media_container_id" B2_S3_REGION)" || true
  media_b2_bucket="$(container_env "$media_container_id" B2_S3_BUCKET)" || true
  media_b2_access="$(container_env "$media_container_id" B2_S3_ACCESS_KEY_ID)" || true
  media_b2_secret="$(container_env "$media_container_id" B2_S3_SECRET_ACCESS_KEY)" || true
  media_r2_endpoint="$(container_env "$media_container_id" CLOUDFLARE_R2_ENDPOINT)" || true
  media_r2_region="$(container_env "$media_container_id" CLOUDFLARE_R2_REGION)" || true
  media_r2_bucket="$(container_env "$media_container_id" CLOUDFLARE_R2_BUCKET)" || true
  media_r2_access="$(container_env "$media_container_id" CLOUDFLARE_R2_ACCESS_KEY_ID)" || true
  media_r2_secret="$(container_env "$media_container_id" CLOUDFLARE_R2_SECRET_ACCESS_KEY)" || true
  media_r2_metadata_bucket="$(container_env "$media_container_id" CLOUDFLARE_R2_METADATA_BUCKET)" || true
  media_vps_metadata_bucket="$(container_env "$media_container_id" VPS_MINIO_METADATA_BUCKET)" || true
  for value in "$media_b2_endpoint" "$media_b2_region" "$media_b2_bucket" "$media_b2_access" "$media_b2_secret" "$media_r2_endpoint" "$media_r2_region" "$media_r2_bucket" "$media_r2_access" "$media_r2_secret" "$media_r2_metadata_bucket" "$media_vps_metadata_bucket"; do
    [[ -n "$value" ]] || die "Data Centre media provider configuration is incomplete; refusing to omit external objects"
  done
  media_external_enabled=true
elif [[ "$(read_env_flag FRESH_MEDIA_ENABLED)" == "true" || "$(read_env_flag FRESH_MEDIA_ENABLED)" == "1" || -n "$(read_env_value DATA_CENTRE_MEDIA_API_URL)" ]]; then
  die "Data Centre media is configured but its runtime container is missing; refusing to omit external objects"
fi

federation_db_ids="$("${DOCKER[@]}" ps -a --filter label=com.docker.compose.service=federation-db --format '{{.ID}}')"
[[ -z "$federation_db_ids" ]] || die "a separate federation-db container exists; back it up and restore-test it before relying on this backup"

video_media_source=""
for service_name in video video-worker; do
  service_ids="$("${DOCKER[@]}" ps -a --filter label=com.docker.compose.project=testagram-data-centre --filter "label=com.docker.compose.service=$service_name" --format '{{.ID}}')"
  if [[ -n "$service_ids" ]]; then
    [[ "$(wc -l <<< "$service_ids" | tr -d ' ')" == "1" ]] || die "multiple Data Centre $service_name containers found; resolve the storage mounts before backup"
    service_id="$(head -n 1 <<< "$service_ids")"
    current_source="$("${DOCKER[@]}" inspect "$service_id" | jq -er '.[0].Mounts[] | select(.Destination == "/media" and .Type == "bind") | .Source' 2>/dev/null || true)"
    [[ -n "$current_source" && -d "$current_source" ]] || die "Data Centre $service_name does not expose a readable bind mount at /media; refusing to omit its files"
    current_source="$(realpath "$current_source")"
    if [[ -n "$video_media_source" && "$video_media_source" != "$current_source" ]]; then
      die "Data Centre video and video-worker use different media sources; reconcile them before backup"
    fi
    video_media_source="$current_source"
  fi
done

storage_root="$(read_env_value TESTAGRAM_STORAGE_ROOT)"
[[ -n "$storage_root" ]] || storage_root="/var/lib/testagram/storage"
if [[ -z "$video_media_source" && -d "$storage_root/media" ]]; then
  video_media_source="$(realpath "$storage_root/media")"
fi
node_storage_source=""
storage_root_for_nodes="$storage_root"
if [[ ! -d "$storage_root_for_nodes/nodes" && -n "$video_media_source" ]]; then
  storage_root_for_nodes="$(dirname "$video_media_source")"
fi
if [[ -d "$storage_root_for_nodes/nodes" ]]; then
  node_storage_source="$(realpath "$storage_root_for_nodes/nodes")"
fi

"${DOCKER[@]}" compose config --quiet
    postgres_user="$("${DOCKER[@]}" compose exec -T postgres printenv POSTGRES_USER | tr -d '\r')"
    postgres_primary_db="$("${DOCKER[@]}" compose exec -T postgres printenv POSTGRES_DB | tr -d '\r')"
    [[ "$postgres_user" =~ ^[A-Za-z0-9_]+$ && "$postgres_primary_db" =~ ^[A-Za-z0-9_]+$ ]] || die "PostgreSQL user/database names are missing or unsupported"
    extra_postgres_roles="$("${DOCKER[@]}" compose exec -T postgres psql -U "$postgres_user" -d "$postgres_primary_db" -Atqc "SELECT rolname FROM pg_roles WHERE rolname !~ '^pg_' AND rolname NOT IN ('postgres', '$postgres_user') ORDER BY rolname")"
    [[ -z "$extra_postgres_roles" ]] || die "additional PostgreSQL roles detected ($extra_postgres_roles); add a tested globals backup/restore before relying on this backup"
    extra_tablespaces="$("${DOCKER[@]}" compose exec -T postgres psql -U "$postgres_user" -d "$postgres_primary_db" -Atqc "SELECT spcname FROM pg_tablespace WHERE spcname NOT IN ('pg_default', 'pg_global') ORDER BY spcname")"
    [[ -z "$extra_tablespaces" ]] || die "user-defined PostgreSQL tablespaces detected ($extra_tablespaces); back up their external paths before relying on this workflow"
    postgres_database_names="$("${DOCKER[@]}" compose exec -T postgres psql -U "$postgres_user" -d "$postgres_primary_db" -Atqc "SELECT datname FROM pg_database WHERE datallowconn AND NOT datistemplate ORDER BY datname")"
    [[ -n "$postgres_database_names" ]] || die "PostgreSQL returned no connectable databases"
    grep -Fxq "$postgres_primary_db" <<< "$postgres_database_names" || die "primary PostgreSQL database is absent from the database inventory"
    if [[ -n "$federation_database_name" ]]; then
      grep -Fxq "$federation_database_name" <<< "$postgres_database_names" || die "Federation API points to a database that is not present in the backed-up PostgreSQL cluster"
    fi
    while IFS= read -r database_name; do
      [[ "$database_name" =~ ^[A-Za-z0-9_]+$ ]] || die "unsupported PostgreSQL database name discovered; refusing unsafe dump path"
    done <<< "$postgres_database_names"
    stage_parent="${BACKUP_STAGE_DIR:-${TMPDIR:-/tmp}}"
    mkdir -p "$stage_parent"
    stage_parent_real="$(realpath "$stage_parent")"
    for source_path in "$video_media_source" "$node_storage_source"; do
      [[ -n "$source_path" ]] || continue
      [[ "$source_path" != "/" ]] || die "refusing to back up the entire filesystem as Data Centre media"
      case "$stage_parent_real/" in
        "$source_path/"*) die "backup staging directory is inside a Data Centre source directory" ;;
      esac
      case "$source_path/" in
        "$stage_parent_real/"*) die "Data Centre source directory is inside the backup staging directory" ;;
      esac
    done

    printf 'Estimating PostgreSQL, MinIO and detected Data Centre files before staging...\n'
    # shellcheck disable=SC2016
    bucket_size_report="$("${DOCKER[@]}" compose run --rm --no-deps \
      --entrypoint /bin/sh object-init -ec '
        mc alias set local http://object-storage:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null
        mc stat "local/$MINIO_BUCKET" --json
        mc stat "local/$PUBLIC_MEDIA_BUCKET" --json
        mc stat "local/$IDENTITY_EVIDENCE_PRIVATE_BUCKET" --json
        mc stat "local/$MINIO_METADATA_BUCKET" --json
      ')"
    minio_bytes="$(jq -s -er 'if length == 4 and all(.[]; (.Usage.totalSize | type) == "number") then map(.Usage.totalSize) | add else error("MinIO did not report sizes for all four buckets") end' <<< "$bucket_size_report")"
    postgres_bytes="$("${DOCKER[@]}" compose exec -T postgres psql -U "$postgres_user" -d "$postgres_primary_db" -Atqc "SELECT COALESCE(SUM(pg_database_size(datname)),0) FROM pg_database WHERE datallowconn AND NOT datistemplate")"
    video_media_bytes=0
    node_storage_bytes=0
    if [[ -n "$video_media_source" ]]; then
      video_media_bytes="$(du -sb "$video_media_source" | awk '{print $1}')"
    fi
    if [[ -n "$node_storage_source" ]]; then
      node_storage_bytes="$(du -sb "$node_storage_source" | awk '{print $1}')"
    fi
    [[ "$minio_bytes" =~ ^[0-9]+$ && "$postgres_bytes" =~ ^[0-9]+$ && "$video_media_bytes" =~ ^[0-9]+$ && "$node_storage_bytes" =~ ^[0-9]+$ ]] || die "could not determine database, MinIO or Data Centre storage sizes"
    required_stage_bytes=$(( (minio_bytes + postgres_bytes + video_media_bytes + node_storage_bytes) * 120 / 100 + 1073741824 ))
    available_stage_bytes="$(df -PB1 "$stage_parent" | awk 'NR == 2 { print $4 }')"
    docker_root="$("${DOCKER[@]}" info --format '{{.DockerRootDir}}')"
    available_docker_bytes="$(df -PB1 "$docker_root" | awk 'NR == 2 { print $4 }')"
    [[ "$available_stage_bytes" =~ ^[0-9]+$ && "$available_docker_bytes" =~ ^[0-9]+$ ]] || die "could not determine free disk space"
    printf 'Estimated backup source: PostgreSQL=%s bytes, MinIO=%s bytes, Data Centre video=%s bytes, node storage=%s bytes; staging free=%s bytes.\n' \
      "$postgres_bytes" "$minio_bytes" "$video_media_bytes" "$node_storage_bytes" "$available_stage_bytes"
    (( available_stage_bytes >= required_stage_bytes )) || die "insufficient staging space; need at least ${required_stage_bytes} bytes free for a safe full copy"
    (( available_docker_bytes >= 1073741824 )) || die "less than 1 GiB remains in Docker storage; refusing to risk production containers"

    stage=$(mktemp -d "${stage_parent%/}/testagram-backup.XXXXXXXX")
    chmod 700 "$stage"
    cleanup() { rm -rf -- "$stage"; }
    trap cleanup EXIT INT TERM

    printf '%s\n' "$postgres_database_names" > "$stage/postgres-databases.txt"
    printf 'Exporting %s PostgreSQL databases...\n' "$(wc -l < "$stage/postgres-databases.txt" | tr -d ' ')"
    while IFS= read -r database_name; do
      dump_path="$stage/postgres-$database_name.dump"
      "${DOCKER[@]}" compose exec -T postgres pg_dump -Fc --no-owner --no-acl -U "$postgres_user" -d "$database_name" > "$dump_path"
      [[ -s "$dump_path" ]] || die "PostgreSQL dump for $database_name is empty"
      "${DOCKER[@]}" compose exec -T postgres pg_restore --list < "$dump_path" > "$dump_path.list"
      [[ -s "$dump_path.list" ]] || die "PostgreSQL dump for $database_name failed archive-list validation"
      chmod 600 "$dump_path" "$dump_path.list"
    done < "$stage/postgres-databases.txt"

    if [[ -n "$video_media_source" ]]; then
      printf 'Copying Data Centre video media into protected staging...\n'
      cp -a -- "$video_media_source" "$stage/data-centre-video-media"
    fi
    if [[ -n "$node_storage_source" ]]; then
      printf 'Copying Data Centre node storage into protected staging...\n'
      cp -a -- "$node_storage_source" "$stage/data-centre-node-storage"
    fi

    printf 'Mirroring all MinIO buckets to protected staging...\n'
    mkdir -p "$stage/minio-private" "$stage/minio-public" "$stage/minio-identity-evidence" "$stage/minio-metadata"
    chmod 700 "$stage/minio-private" "$stage/minio-public" "$stage/minio-identity-evidence" "$stage/minio-metadata"
    # shellcheck disable=SC2016
    "${DOCKER[@]}" compose run --rm --no-deps \
      -v "$stage:/backup" \
      --entrypoint /bin/sh object-init -ec '
        mc alias set local http://object-storage:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null
        mc mirror --preserve local/"$MINIO_BUCKET" /backup/minio-private >/dev/null
        mc mirror --preserve local/"$PUBLIC_MEDIA_BUCKET" /backup/minio-public >/dev/null
        mc mirror --preserve local/"$IDENTITY_EVIDENCE_PRIVATE_BUCKET" /backup/minio-identity-evidence >/dev/null
        mc mirror --preserve local/"$MINIO_METADATA_BUCKET" /backup/minio-metadata >/dev/null
      '
    printf '%s\n' \
      "Testagram self-hosted backup" \
      "created_utc=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      "postgres_user=$postgres_user" \
      "postgres_primary_database=$postgres_primary_db" \
      "postgres_database_count=$(wc -l < "$stage/postgres-databases.txt" | tr -d ' ')" \
      "postgres_source_bytes=$postgres_bytes" \
      "private_bucket=${MINIO_BUCKET:-testagram-private-media}" \
      "public_bucket=${PUBLIC_MEDIA_BUCKET:-testagram-public-media}" \
      "identity_evidence_bucket=${IDENTITY_EVIDENCE_PRIVATE_BUCKET:-testagram-identity-evidence-private}" \
      "metadata_bucket=${MINIO_METADATA_BUCKET:-testagram-media-metadata}" \
      "data_centre_video_media_included=$([[ -n "$video_media_source" ]] && echo true || echo false)" \
      "data_centre_video_media_source=$video_media_source" \
      "data_centre_video_media_bytes=$video_media_bytes" \
      "data_centre_node_storage_included=$([[ -n "$node_storage_source" ]] && echo true || echo false)" \
      "data_centre_node_storage_source=$node_storage_source" \
      "data_centre_node_storage_bytes=$node_storage_bytes" \
      "repository=${RESTIC_REPOSITORY}" > "$stage/manifest.txt"
    chmod 600 "$stage/manifest.txt"
    printf 'Writing encrypted restic snapshot...\n'
    restic backup --host testagram-self-hosted --tag testagram --tag automated "$stage"
    printf 'Verifying repository metadata and a data subset...\n'
    restic check --read-data-subset=1/20
    printf 'Backup complete. Temporary plaintext staging will now be removed.\n'
    ;;
  check)
    restic snapshots --host testagram-self-hosted --tag testagram
    restic check --read-data-subset=1/20
    ;;
  prune)
    restic forget --host testagram-self-hosted --tag testagram \
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
\\t' read -r media_database_user media_database_name <<< "$media_identity"
  [[ "$media_vps_metadata_bucket" == "$(read_env_value MINIO_METADATA_BUCKET)" ]] || die "Data Centre metadata primary bucket differs from the local MinIO bucket included in this backup"
  backup_identity="$(python3 - "$RESTIC_REPOSITORY" <<'PY'
import sys
from urllib.parse import urlparse
value = sys.argv[1]
if not value.startswith("s3:"):
    sys.exit(1)
parsed = urlparse(value[3:])
parts = [part for part in parsed.path.split("/") if part]
if parsed.scheme != "https" or not parsed.hostname or not parts:
    sys.exit(1)
print(parsed.hostname.lower() + "|" + parts[0])
PY
  )" || die "Restic destination must be an S3 HTTPS repository with a bucket path"
  IFS=
data_centre_media_ids="$("${DOCKER[@]}" ps -a --filter label=com.docker.compose.project=testagram-data-centre --filter label=com.docker.compose.service=media --format '{{.ID}}')"
media_database_url=""
media_database_name=""
media_database_user=""
media_b2_endpoint=""
media_b2_region=""
media_b2_bucket=""
media_b2_access=""
media_b2_secret=""
media_r2_endpoint=""
media_r2_region=""
media_r2_bucket=""
media_r2_access=""
media_r2_secret=""
media_r2_metadata_bucket=""
media_vps_metadata_bucket=""
if [[ -n "$data_centre_media_ids" ]]; then
  [[ "$(wc -l <<< "$data_centre_media_ids" | tr -d ' ')" == "1" ]] || die "multiple Data Centre media containers found"
  media_container_id="$(head -n 1 <<< "$data_centre_media_ids")"
  media_running="$("${DOCKER[@]}" inspect "$media_container_id" | jq -er '.[0].State.Running')"
  [[ "$media_running" == "true" ]] || die "Data Centre media container exists but is not running; refusing an incomplete media backup"
  [[ "$(read_env_flag FRESH_MEDIA_ENABLED)" == "true" || "$(read_env_flag FRESH_MEDIA_ENABLED)" == "1" ]] || die "Data Centre media container is running while FRESH_MEDIA_ENABLED is not true"
  [[ "$(read_env_value DATA_CENTRE_MEDIA_API_URL)" == "http://media:8096" ]] || die "Backend is not configured for the active private Data Centre media service"
  media_database_url="$(container_env "$media_container_id" MEDIA_DATABASE_URL)" || die "Data Centre media container has no readable MEDIA_DATABASE_URL"
  media_b2_endpoint="$(container_env "$media_container_id" B2_S3_ENDPOINT)" || true
  media_b2_region="$(container_env "$media_container_id" B2_S3_REGION)" || true
  media_b2_bucket="$(container_env "$media_container_id" B2_S3_BUCKET)" || true
  media_b2_access="$(container_env "$media_container_id" B2_S3_ACCESS_KEY_ID)" || true
  media_b2_secret="$(container_env "$media_container_id" B2_S3_SECRET_ACCESS_KEY)" || true
  media_r2_endpoint="$(container_env "$media_container_id" CLOUDFLARE_R2_ENDPOINT)" || true
  media_r2_region="$(container_env "$media_container_id" CLOUDFLARE_R2_REGION)" || true
  media_r2_bucket="$(container_env "$media_container_id" CLOUDFLARE_R2_BUCKET)" || true
  media_r2_access="$(container_env "$media_container_id" CLOUDFLARE_R2_ACCESS_KEY_ID)" || true
  media_r2_secret="$(container_env "$media_container_id" CLOUDFLARE_R2_SECRET_ACCESS_KEY)" || true
  media_r2_metadata_bucket="$(container_env "$media_container_id" CLOUDFLARE_R2_METADATA_BUCKET)" || true
  media_vps_metadata_bucket="$(container_env "$media_container_id" VPS_MINIO_METADATA_BUCKET)" || true
  for value in "$media_b2_endpoint" "$media_b2_region" "$media_b2_bucket" "$media_b2_access" "$media_b2_secret" "$media_r2_endpoint" "$media_r2_region" "$media_r2_bucket" "$media_r2_access" "$media_r2_secret" "$media_r2_metadata_bucket" "$media_vps_metadata_bucket"; do
    [[ -n "$value" ]] || die "Data Centre media provider configuration is incomplete; refusing to omit external objects"
  done
  media_external_enabled=true
elif [[ "$(read_env_flag FRESH_MEDIA_ENABLED)" == "true" || "$(read_env_flag FRESH_MEDIA_ENABLED)" == "1" || -n "$(read_env_value DATA_CENTRE_MEDIA_API_URL)" ]]; then
  die "Data Centre media is configured but its runtime container is missing; refusing to omit external objects"
fi

federation_db_ids="$("${DOCKER[@]}" ps -a --filter label=com.docker.compose.service=federation-db --format '{{.ID}}')"
[[ -z "$federation_db_ids" ]] || die "a separate federation-db container exists; back it up and restore-test it before relying on this backup"

video_media_source=""
for service_name in video video-worker; do
  service_ids="$("${DOCKER[@]}" ps -a --filter label=com.docker.compose.project=testagram-data-centre --filter "label=com.docker.compose.service=$service_name" --format '{{.ID}}')"
  if [[ -n "$service_ids" ]]; then
    [[ "$(wc -l <<< "$service_ids" | tr -d ' ')" == "1" ]] || die "multiple Data Centre $service_name containers found; resolve the storage mounts before backup"
    service_id="$(head -n 1 <<< "$service_ids")"
    current_source="$("${DOCKER[@]}" inspect "$service_id" | jq -er '.[0].Mounts[] | select(.Destination == "/media" and .Type == "bind") | .Source' 2>/dev/null || true)"
    [[ -n "$current_source" && -d "$current_source" ]] || die "Data Centre $service_name does not expose a readable bind mount at /media; refusing to omit its files"
    current_source="$(realpath "$current_source")"
    if [[ -n "$video_media_source" && "$video_media_source" != "$current_source" ]]; then
      die "Data Centre video and video-worker use different media sources; reconcile them before backup"
    fi
    video_media_source="$current_source"
  fi
done

storage_root="$(read_env_value TESTAGRAM_STORAGE_ROOT)"
[[ -n "$storage_root" ]] || storage_root="/var/lib/testagram/storage"
if [[ -z "$video_media_source" && -d "$storage_root/media" ]]; then
  video_media_source="$(realpath "$storage_root/media")"
fi
node_storage_source=""
storage_root_for_nodes="$storage_root"
if [[ ! -d "$storage_root_for_nodes/nodes" && -n "$video_media_source" ]]; then
  storage_root_for_nodes="$(dirname "$video_media_source")"
fi
if [[ -d "$storage_root_for_nodes/nodes" ]]; then
  node_storage_source="$(realpath "$storage_root_for_nodes/nodes")"
fi

"${DOCKER[@]}" compose config --quiet
    postgres_user="$("${DOCKER[@]}" compose exec -T postgres printenv POSTGRES_USER | tr -d '\r')"
    postgres_primary_db="$("${DOCKER[@]}" compose exec -T postgres printenv POSTGRES_DB | tr -d '\r')"
    [[ "$postgres_user" =~ ^[A-Za-z0-9_]+$ && "$postgres_primary_db" =~ ^[A-Za-z0-9_]+$ ]] || die "PostgreSQL user/database names are missing or unsupported"
    extra_postgres_roles="$("${DOCKER[@]}" compose exec -T postgres psql -U "$postgres_user" -d "$postgres_primary_db" -Atqc "SELECT rolname FROM pg_roles WHERE rolname !~ '^pg_' AND rolname NOT IN ('postgres', '$postgres_user') ORDER BY rolname")"
    [[ -z "$extra_postgres_roles" ]] || die "additional PostgreSQL roles detected ($extra_postgres_roles); add a tested globals backup/restore before relying on this backup"
    extra_tablespaces="$("${DOCKER[@]}" compose exec -T postgres psql -U "$postgres_user" -d "$postgres_primary_db" -Atqc "SELECT spcname FROM pg_tablespace WHERE spcname NOT IN ('pg_default', 'pg_global') ORDER BY spcname")"
    [[ -z "$extra_tablespaces" ]] || die "user-defined PostgreSQL tablespaces detected ($extra_tablespaces); back up their external paths before relying on this workflow"
    postgres_database_names="$("${DOCKER[@]}" compose exec -T postgres psql -U "$postgres_user" -d "$postgres_primary_db" -Atqc "SELECT datname FROM pg_database WHERE datallowconn AND NOT datistemplate ORDER BY datname")"
    [[ -n "$postgres_database_names" ]] || die "PostgreSQL returned no connectable databases"
    grep -Fxq "$postgres_primary_db" <<< "$postgres_database_names" || die "primary PostgreSQL database is absent from the database inventory"
    if [[ -n "$federation_database_name" ]]; then
      grep -Fxq "$federation_database_name" <<< "$postgres_database_names" || die "Federation API points to a database that is not present in the backed-up PostgreSQL cluster"
    fi
    while IFS= read -r database_name; do
      [[ "$database_name" =~ ^[A-Za-z0-9_]+$ ]] || die "unsupported PostgreSQL database name discovered; refusing unsafe dump path"
    done <<< "$postgres_database_names"
    stage_parent="${BACKUP_STAGE_DIR:-${TMPDIR:-/tmp}}"
    mkdir -p "$stage_parent"
    stage_parent_real="$(realpath "$stage_parent")"
    for source_path in "$video_media_source" "$node_storage_source"; do
      [[ -n "$source_path" ]] || continue
      [[ "$source_path" != "/" ]] || die "refusing to back up the entire filesystem as Data Centre media"
      case "$stage_parent_real/" in
        "$source_path/"*) die "backup staging directory is inside a Data Centre source directory" ;;
      esac
      case "$source_path/" in
        "$stage_parent_real/"*) die "Data Centre source directory is inside the backup staging directory" ;;
      esac
    done

    printf 'Estimating PostgreSQL, MinIO and detected Data Centre files before staging...\n'
    # shellcheck disable=SC2016
    bucket_size_report="$("${DOCKER[@]}" compose run --rm --no-deps \
      --entrypoint /bin/sh object-init -ec '
        mc alias set local http://object-storage:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null
        mc stat "local/$MINIO_BUCKET" --json
        mc stat "local/$PUBLIC_MEDIA_BUCKET" --json
        mc stat "local/$IDENTITY_EVIDENCE_PRIVATE_BUCKET" --json
        mc stat "local/$MINIO_METADATA_BUCKET" --json
      ')"
    minio_bytes="$(jq -s -er 'if length == 4 and all(.[]; (.Usage.totalSize | type) == "number") then map(.Usage.totalSize) | add else error("MinIO did not report sizes for all four buckets") end' <<< "$bucket_size_report")"
    postgres_bytes="$("${DOCKER[@]}" compose exec -T postgres psql -U "$postgres_user" -d "$postgres_primary_db" -Atqc "SELECT COALESCE(SUM(pg_database_size(datname)),0) FROM pg_database WHERE datallowconn AND NOT datistemplate")"
    video_media_bytes=0
    node_storage_bytes=0
    if [[ -n "$video_media_source" ]]; then
      video_media_bytes="$(du -sb "$video_media_source" | awk '{print $1}')"
    fi
    if [[ -n "$node_storage_source" ]]; then
      node_storage_bytes="$(du -sb "$node_storage_source" | awk '{print $1}')"
    fi
    [[ "$minio_bytes" =~ ^[0-9]+$ && "$postgres_bytes" =~ ^[0-9]+$ && "$video_media_bytes" =~ ^[0-9]+$ && "$node_storage_bytes" =~ ^[0-9]+$ ]] || die "could not determine database, MinIO or Data Centre storage sizes"
    required_stage_bytes=$(( (minio_bytes + postgres_bytes + video_media_bytes + node_storage_bytes) * 120 / 100 + 1073741824 ))
    available_stage_bytes="$(df -PB1 "$stage_parent" | awk 'NR == 2 { print $4 }')"
    docker_root="$("${DOCKER[@]}" info --format '{{.DockerRootDir}}')"
    available_docker_bytes="$(df -PB1 "$docker_root" | awk 'NR == 2 { print $4 }')"
    [[ "$available_stage_bytes" =~ ^[0-9]+$ && "$available_docker_bytes" =~ ^[0-9]+$ ]] || die "could not determine free disk space"
    printf 'Estimated backup source: PostgreSQL=%s bytes, MinIO=%s bytes, Data Centre video=%s bytes, node storage=%s bytes; staging free=%s bytes.\n' \
      "$postgres_bytes" "$minio_bytes" "$video_media_bytes" "$node_storage_bytes" "$available_stage_bytes"
    (( available_stage_bytes >= required_stage_bytes )) || die "insufficient staging space; need at least ${required_stage_bytes} bytes free for a safe full copy"
    (( available_docker_bytes >= 1073741824 )) || die "less than 1 GiB remains in Docker storage; refusing to risk production containers"

    stage=$(mktemp -d "${stage_parent%/}/testagram-backup.XXXXXXXX")
    chmod 700 "$stage"
    cleanup() { rm -rf -- "$stage"; }
    trap cleanup EXIT INT TERM

    printf '%s\n' "$postgres_database_names" > "$stage/postgres-databases.txt"
    printf 'Exporting %s PostgreSQL databases...\n' "$(wc -l < "$stage/postgres-databases.txt" | tr -d ' ')"
    while IFS= read -r database_name; do
      dump_path="$stage/postgres-$database_name.dump"
      "${DOCKER[@]}" compose exec -T postgres pg_dump -Fc --no-owner --no-acl -U "$postgres_user" -d "$database_name" > "$dump_path"
      [[ -s "$dump_path" ]] || die "PostgreSQL dump for $database_name is empty"
      "${DOCKER[@]}" compose exec -T postgres pg_restore --list < "$dump_path" > "$dump_path.list"
      [[ -s "$dump_path.list" ]] || die "PostgreSQL dump for $database_name failed archive-list validation"
      chmod 600 "$dump_path" "$dump_path.list"
    done < "$stage/postgres-databases.txt"

    if [[ -n "$video_media_source" ]]; then
      printf 'Copying Data Centre video media into protected staging...\n'
      cp -a -- "$video_media_source" "$stage/data-centre-video-media"
    fi
    if [[ -n "$node_storage_source" ]]; then
      printf 'Copying Data Centre node storage into protected staging...\n'
      cp -a -- "$node_storage_source" "$stage/data-centre-node-storage"
    fi

    printf 'Mirroring all MinIO buckets to protected staging...\n'
    mkdir -p "$stage/minio-private" "$stage/minio-public" "$stage/minio-identity-evidence" "$stage/minio-metadata"
    chmod 700 "$stage/minio-private" "$stage/minio-public" "$stage/minio-identity-evidence" "$stage/minio-metadata"
    # shellcheck disable=SC2016
    "${DOCKER[@]}" compose run --rm --no-deps \
      -v "$stage:/backup" \
      --entrypoint /bin/sh object-init -ec '
        mc alias set local http://object-storage:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null
        mc mirror --preserve local/"$MINIO_BUCKET" /backup/minio-private >/dev/null
        mc mirror --preserve local/"$PUBLIC_MEDIA_BUCKET" /backup/minio-public >/dev/null
        mc mirror --preserve local/"$IDENTITY_EVIDENCE_PRIVATE_BUCKET" /backup/minio-identity-evidence >/dev/null
        mc mirror --preserve local/"$MINIO_METADATA_BUCKET" /backup/minio-metadata >/dev/null
      '
    printf '%s\n' \
      "Testagram self-hosted backup" \
      "created_utc=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      "postgres_user=$postgres_user" \
      "postgres_primary_database=$postgres_primary_db" \
      "postgres_database_count=$(wc -l < "$stage/postgres-databases.txt" | tr -d ' ')" \
      "postgres_source_bytes=$postgres_bytes" \
      "private_bucket=${MINIO_BUCKET:-testagram-private-media}" \
      "public_bucket=${PUBLIC_MEDIA_BUCKET:-testagram-public-media}" \
      "identity_evidence_bucket=${IDENTITY_EVIDENCE_PRIVATE_BUCKET:-testagram-identity-evidence-private}" \
      "metadata_bucket=${MINIO_METADATA_BUCKET:-testagram-media-metadata}" \
      "data_centre_video_media_included=$([[ -n "$video_media_source" ]] && echo true || echo false)" \
      "data_centre_video_media_source=$video_media_source" \
      "data_centre_video_media_bytes=$video_media_bytes" \
      "data_centre_node_storage_included=$([[ -n "$node_storage_source" ]] && echo true || echo false)" \
      "data_centre_node_storage_source=$node_storage_source" \
      "data_centre_node_storage_bytes=$node_storage_bytes" \
      "repository=${RESTIC_REPOSITORY}" > "$stage/manifest.txt"
    chmod 600 "$stage/manifest.txt"
    printf 'Writing encrypted restic snapshot...\n'
    restic backup --host testagram-self-hosted --tag testagram --tag automated "$stage"
    printf 'Verifying repository metadata and a data subset...\n'
    restic check --read-data-subset=1/20
    printf 'Backup complete. Temporary plaintext staging will now be removed.\n'
    ;;
  check)
    restic snapshots --host testagram-self-hosted --tag testagram
    restic check --read-data-subset=1/20
    ;;
  prune)
    restic forget --host testagram-self-hosted --tag testagram \
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
\\t' read -r backup_host backup_bucket <<< "$backup_identity"
  for source in "$media_b2_endpoint|$media_b2_bucket" "$media_r2_endpoint|$media_r2_bucket" "$media_r2_endpoint|$media_r2_metadata_bucket"; do
    source_host="${source%%|*}"
    source_bucket="${source#*|}"
    source_host="$(python3 - "$source_host" <<'PY'
import sys
from urllib.parse import urlparse
print((urlparse(sys.argv[1]).hostname or "").lower())
PY
    )"
    [[ "$backup_host" != "$source_host" || "$backup_bucket" != "$source_bucket" ]] || die "Restic backup destination must be a separate bucket from active B2/R2 media storage"
  done
fi

data_centre_media_ids="$("${DOCKER[@]}" ps -a --filter label=com.docker.compose.project=testagram-data-centre --filter label=com.docker.compose.service=media --format '{{.ID}}')"
media_database_url=""
media_database_name=""
media_database_user=""
media_b2_endpoint=""
media_b2_region=""
media_b2_bucket=""
media_b2_access=""
media_b2_secret=""
media_r2_endpoint=""
media_r2_region=""
media_r2_bucket=""
media_r2_access=""
media_r2_secret=""
media_r2_metadata_bucket=""
media_vps_metadata_bucket=""
if [[ -n "$data_centre_media_ids" ]]; then
  [[ "$(wc -l <<< "$data_centre_media_ids" | tr -d ' ')" == "1" ]] || die "multiple Data Centre media containers found"
  media_container_id="$(head -n 1 <<< "$data_centre_media_ids")"
  media_running="$("${DOCKER[@]}" inspect "$media_container_id" | jq -er '.[0].State.Running')"
  [[ "$media_running" == "true" ]] || die "Data Centre media container exists but is not running; refusing an incomplete media backup"
  [[ "$(read_env_flag FRESH_MEDIA_ENABLED)" == "true" || "$(read_env_flag FRESH_MEDIA_ENABLED)" == "1" ]] || die "Data Centre media container is running while FRESH_MEDIA_ENABLED is not true"
  [[ "$(read_env_value DATA_CENTRE_MEDIA_API_URL)" == "http://media:8096" ]] || die "Backend is not configured for the active private Data Centre media service"
  media_database_url="$(container_env "$media_container_id" MEDIA_DATABASE_URL)" || die "Data Centre media container has no readable MEDIA_DATABASE_URL"
  media_b2_endpoint="$(container_env "$media_container_id" B2_S3_ENDPOINT)" || true
  media_b2_region="$(container_env "$media_container_id" B2_S3_REGION)" || true
  media_b2_bucket="$(container_env "$media_container_id" B2_S3_BUCKET)" || true
  media_b2_access="$(container_env "$media_container_id" B2_S3_ACCESS_KEY_ID)" || true
  media_b2_secret="$(container_env "$media_container_id" B2_S3_SECRET_ACCESS_KEY)" || true
  media_r2_endpoint="$(container_env "$media_container_id" CLOUDFLARE_R2_ENDPOINT)" || true
  media_r2_region="$(container_env "$media_container_id" CLOUDFLARE_R2_REGION)" || true
  media_r2_bucket="$(container_env "$media_container_id" CLOUDFLARE_R2_BUCKET)" || true
  media_r2_access="$(container_env "$media_container_id" CLOUDFLARE_R2_ACCESS_KEY_ID)" || true
  media_r2_secret="$(container_env "$media_container_id" CLOUDFLARE_R2_SECRET_ACCESS_KEY)" || true
  media_r2_metadata_bucket="$(container_env "$media_container_id" CLOUDFLARE_R2_METADATA_BUCKET)" || true
  media_vps_metadata_bucket="$(container_env "$media_container_id" VPS_MINIO_METADATA_BUCKET)" || true
  for value in "$media_b2_endpoint" "$media_b2_region" "$media_b2_bucket" "$media_b2_access" "$media_b2_secret" "$media_r2_endpoint" "$media_r2_region" "$media_r2_bucket" "$media_r2_access" "$media_r2_secret" "$media_r2_metadata_bucket" "$media_vps_metadata_bucket"; do
    [[ -n "$value" ]] || die "Data Centre media provider configuration is incomplete; refusing to omit external objects"
  done
  media_external_enabled=true
elif [[ "$(read_env_flag FRESH_MEDIA_ENABLED)" == "true" || "$(read_env_flag FRESH_MEDIA_ENABLED)" == "1" || -n "$(read_env_value DATA_CENTRE_MEDIA_API_URL)" ]]; then
  die "Data Centre media is configured but its runtime container is missing; refusing to omit external objects"
fi

federation_db_ids="$("${DOCKER[@]}" ps -a --filter label=com.docker.compose.service=federation-db --format '{{.ID}}')"
[[ -z "$federation_db_ids" ]] || die "a separate federation-db container exists; back it up and restore-test it before relying on this backup"

video_media_source=""
for service_name in video video-worker; do
  service_ids="$("${DOCKER[@]}" ps -a --filter label=com.docker.compose.project=testagram-data-centre --filter "label=com.docker.compose.service=$service_name" --format '{{.ID}}')"
  if [[ -n "$service_ids" ]]; then
    [[ "$(wc -l <<< "$service_ids" | tr -d ' ')" == "1" ]] || die "multiple Data Centre $service_name containers found; resolve the storage mounts before backup"
    service_id="$(head -n 1 <<< "$service_ids")"
    current_source="$("${DOCKER[@]}" inspect "$service_id" | jq -er '.[0].Mounts[] | select(.Destination == "/media" and .Type == "bind") | .Source' 2>/dev/null || true)"
    [[ -n "$current_source" && -d "$current_source" ]] || die "Data Centre $service_name does not expose a readable bind mount at /media; refusing to omit its files"
    current_source="$(realpath "$current_source")"
    if [[ -n "$video_media_source" && "$video_media_source" != "$current_source" ]]; then
      die "Data Centre video and video-worker use different media sources; reconcile them before backup"
    fi
    video_media_source="$current_source"
  fi
done

storage_root="$(read_env_value TESTAGRAM_STORAGE_ROOT)"
[[ -n "$storage_root" ]] || storage_root="/var/lib/testagram/storage"
if [[ -z "$video_media_source" && -d "$storage_root/media" ]]; then
  video_media_source="$(realpath "$storage_root/media")"
fi
node_storage_source=""
storage_root_for_nodes="$storage_root"
if [[ ! -d "$storage_root_for_nodes/nodes" && -n "$video_media_source" ]]; then
  storage_root_for_nodes="$(dirname "$video_media_source")"
fi
if [[ -d "$storage_root_for_nodes/nodes" ]]; then
  node_storage_source="$(realpath "$storage_root_for_nodes/nodes")"
fi

"${DOCKER[@]}" compose config --quiet
    postgres_user="$("${DOCKER[@]}" compose exec -T postgres printenv POSTGRES_USER | tr -d '\r')"
    postgres_primary_db="$("${DOCKER[@]}" compose exec -T postgres printenv POSTGRES_DB | tr -d '\r')"
    [[ "$postgres_user" =~ ^[A-Za-z0-9_]+$ && "$postgres_primary_db" =~ ^[A-Za-z0-9_]+$ ]] || die "PostgreSQL user/database names are missing or unsupported"
    extra_postgres_roles="$("${DOCKER[@]}" compose exec -T postgres psql -U "$postgres_user" -d "$postgres_primary_db" -Atqc "SELECT rolname FROM pg_roles WHERE rolname !~ '^pg_' AND rolname NOT IN ('postgres', '$postgres_user') ORDER BY rolname")"
    [[ -z "$extra_postgres_roles" ]] || die "additional PostgreSQL roles detected ($extra_postgres_roles); add a tested globals backup/restore before relying on this backup"
    extra_tablespaces="$("${DOCKER[@]}" compose exec -T postgres psql -U "$postgres_user" -d "$postgres_primary_db" -Atqc "SELECT spcname FROM pg_tablespace WHERE spcname NOT IN ('pg_default', 'pg_global') ORDER BY spcname")"
    [[ -z "$extra_tablespaces" ]] || die "user-defined PostgreSQL tablespaces detected ($extra_tablespaces); back up their external paths before relying on this workflow"
    postgres_database_names="$("${DOCKER[@]}" compose exec -T postgres psql -U "$postgres_user" -d "$postgres_primary_db" -Atqc "SELECT datname FROM pg_database WHERE datallowconn AND NOT datistemplate ORDER BY datname")"
    [[ -n "$postgres_database_names" ]] || die "PostgreSQL returned no connectable databases"
    grep -Fxq "$postgres_primary_db" <<< "$postgres_database_names" || die "primary PostgreSQL database is absent from the database inventory"
    if [[ -n "$federation_database_name" ]]; then
      grep -Fxq "$federation_database_name" <<< "$postgres_database_names" || die "Federation API points to a database that is not present in the backed-up PostgreSQL cluster"
    fi
    while IFS= read -r database_name; do
      [[ "$database_name" =~ ^[A-Za-z0-9_]+$ ]] || die "unsupported PostgreSQL database name discovered; refusing unsafe dump path"
    done <<< "$postgres_database_names"
    stage_parent="${BACKUP_STAGE_DIR:-${TMPDIR:-/tmp}}"
    mkdir -p "$stage_parent"
    stage_parent_real="$(realpath "$stage_parent")"
    for source_path in "$video_media_source" "$node_storage_source"; do
      [[ -n "$source_path" ]] || continue
      [[ "$source_path" != "/" ]] || die "refusing to back up the entire filesystem as Data Centre media"
      case "$stage_parent_real/" in
        "$source_path/"*) die "backup staging directory is inside a Data Centre source directory" ;;
      esac
      case "$source_path/" in
        "$stage_parent_real/"*) die "Data Centre source directory is inside the backup staging directory" ;;
      esac
    done

    printf 'Estimating PostgreSQL, MinIO and detected Data Centre files before staging...\n'
    # shellcheck disable=SC2016
    bucket_size_report="$("${DOCKER[@]}" compose run --rm --no-deps \
      --entrypoint /bin/sh object-init -ec '
        mc alias set local http://object-storage:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null
        mc stat "local/$MINIO_BUCKET" --json
        mc stat "local/$PUBLIC_MEDIA_BUCKET" --json
        mc stat "local/$IDENTITY_EVIDENCE_PRIVATE_BUCKET" --json
        mc stat "local/$MINIO_METADATA_BUCKET" --json
      ')"
    minio_bytes="$(jq -s -er 'if length == 4 and all(.[]; (.Usage.totalSize | type) == "number") then map(.Usage.totalSize) | add else error("MinIO did not report sizes for all four buckets") end' <<< "$bucket_size_report")"
    postgres_bytes="$("${DOCKER[@]}" compose exec -T postgres psql -U "$postgres_user" -d "$postgres_primary_db" -Atqc "SELECT COALESCE(SUM(pg_database_size(datname)),0) FROM pg_database WHERE datallowconn AND NOT datistemplate")"
    video_media_bytes=0
    node_storage_bytes=0
    if [[ -n "$video_media_source" ]]; then
      video_media_bytes="$(du -sb "$video_media_source" | awk '{print $1}')"
    fi
    if [[ -n "$node_storage_source" ]]; then
      node_storage_bytes="$(du -sb "$node_storage_source" | awk '{print $1}')"
    fi
    [[ "$minio_bytes" =~ ^[0-9]+$ && "$postgres_bytes" =~ ^[0-9]+$ && "$video_media_bytes" =~ ^[0-9]+$ && "$node_storage_bytes" =~ ^[0-9]+$ ]] || die "could not determine database, MinIO or Data Centre storage sizes"
    required_stage_bytes=$(( (minio_bytes + postgres_bytes + video_media_bytes + node_storage_bytes) * 120 / 100 + 1073741824 ))
    available_stage_bytes="$(df -PB1 "$stage_parent" | awk 'NR == 2 { print $4 }')"
    docker_root="$("${DOCKER[@]}" info --format '{{.DockerRootDir}}')"
    available_docker_bytes="$(df -PB1 "$docker_root" | awk 'NR == 2 { print $4 }')"
    [[ "$available_stage_bytes" =~ ^[0-9]+$ && "$available_docker_bytes" =~ ^[0-9]+$ ]] || die "could not determine free disk space"
    printf 'Estimated backup source: PostgreSQL=%s bytes, MinIO=%s bytes, Data Centre video=%s bytes, node storage=%s bytes; staging free=%s bytes.\n' \
      "$postgres_bytes" "$minio_bytes" "$video_media_bytes" "$node_storage_bytes" "$available_stage_bytes"
    (( available_stage_bytes >= required_stage_bytes )) || die "insufficient staging space; need at least ${required_stage_bytes} bytes free for a safe full copy"
    (( available_docker_bytes >= 1073741824 )) || die "less than 1 GiB remains in Docker storage; refusing to risk production containers"

    stage=$(mktemp -d "${stage_parent%/}/testagram-backup.XXXXXXXX")
    chmod 700 "$stage"
    cleanup() { rm -rf -- "$stage"; }
    trap cleanup EXIT INT TERM

    printf '%s\n' "$postgres_database_names" > "$stage/postgres-databases.txt"
    printf 'Exporting %s PostgreSQL databases...\n' "$(wc -l < "$stage/postgres-databases.txt" | tr -d ' ')"
    while IFS= read -r database_name; do
      dump_path="$stage/postgres-$database_name.dump"
      "${DOCKER[@]}" compose exec -T postgres pg_dump -Fc --no-owner --no-acl -U "$postgres_user" -d "$database_name" > "$dump_path"
      [[ -s "$dump_path" ]] || die "PostgreSQL dump for $database_name is empty"
      "${DOCKER[@]}" compose exec -T postgres pg_restore --list < "$dump_path" > "$dump_path.list"
      [[ -s "$dump_path.list" ]] || die "PostgreSQL dump for $database_name failed archive-list validation"
      chmod 600 "$dump_path" "$dump_path.list"
    done < "$stage/postgres-databases.txt"

    if [[ -n "$video_media_source" ]]; then
      printf 'Copying Data Centre video media into protected staging...\n'
      cp -a -- "$video_media_source" "$stage/data-centre-video-media"
    fi
    if [[ -n "$node_storage_source" ]]; then
      printf 'Copying Data Centre node storage into protected staging...\n'
      cp -a -- "$node_storage_source" "$stage/data-centre-node-storage"
    fi

    printf 'Mirroring all MinIO buckets to protected staging...\n'
    mkdir -p "$stage/minio-private" "$stage/minio-public" "$stage/minio-identity-evidence" "$stage/minio-metadata"
    chmod 700 "$stage/minio-private" "$stage/minio-public" "$stage/minio-identity-evidence" "$stage/minio-metadata"
    # shellcheck disable=SC2016
    "${DOCKER[@]}" compose run --rm --no-deps \
      -v "$stage:/backup" \
      --entrypoint /bin/sh object-init -ec '
        mc alias set local http://object-storage:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null
        mc mirror --preserve local/"$MINIO_BUCKET" /backup/minio-private >/dev/null
        mc mirror --preserve local/"$PUBLIC_MEDIA_BUCKET" /backup/minio-public >/dev/null
        mc mirror --preserve local/"$IDENTITY_EVIDENCE_PRIVATE_BUCKET" /backup/minio-identity-evidence >/dev/null
        mc mirror --preserve local/"$MINIO_METADATA_BUCKET" /backup/minio-metadata >/dev/null
      '
    printf '%s\n' \
      "Testagram self-hosted backup" \
      "created_utc=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      "postgres_user=$postgres_user" \
      "postgres_primary_database=$postgres_primary_db" \
      "postgres_database_count=$(wc -l < "$stage/postgres-databases.txt" | tr -d ' ')" \
      "postgres_source_bytes=$postgres_bytes" \
      "private_bucket=${MINIO_BUCKET:-testagram-private-media}" \
      "public_bucket=${PUBLIC_MEDIA_BUCKET:-testagram-public-media}" \
      "identity_evidence_bucket=${IDENTITY_EVIDENCE_PRIVATE_BUCKET:-testagram-identity-evidence-private}" \
      "metadata_bucket=${MINIO_METADATA_BUCKET:-testagram-media-metadata}" \
      "data_centre_video_media_included=$([[ -n "$video_media_source" ]] && echo true || echo false)" \
      "data_centre_video_media_source=$video_media_source" \
      "data_centre_video_media_bytes=$video_media_bytes" \
      "data_centre_node_storage_included=$([[ -n "$node_storage_source" ]] && echo true || echo false)" \
      "data_centre_node_storage_source=$node_storage_source" \
      "data_centre_node_storage_bytes=$node_storage_bytes" \
      "repository=${RESTIC_REPOSITORY}" > "$stage/manifest.txt"
    chmod 600 "$stage/manifest.txt"
    printf 'Writing encrypted restic snapshot...\n'
    restic backup --host testagram-self-hosted --tag testagram --tag automated "$stage"
    printf 'Verifying repository metadata and a data subset...\n'
    restic check --read-data-subset=1/20
    printf 'Backup complete. Temporary plaintext staging will now be removed.\n'
    ;;
  check)
    restic snapshots --host testagram-self-hosted --tag testagram
    restic check --read-data-subset=1/20
    ;;
  prune)
    restic forget --host testagram-self-hosted --tag testagram \
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
\\t' \
        -c "SELECT namespace, object_key, COALESCE(size_bytes,0), COALESCE(trim(sha256),''), status FROM media_objects ORDER BY namespace, object_key" > "$prefix.objects"
      "${DOCKER[@]}" compose exec -T postgres psql -v ON_ERROR_STOP=1 -U "$postgres_user" -d "$media_database_name" -At \
        -c "SELECT status || ':' || count(*) FROM media_outbox GROUP BY status ORDER BY status" > "$prefix.outbox"
      pending_objects="$("${DOCKER[@]}" compose exec -T postgres psql -v ON_ERROR_STOP=1 -U "$postgres_user" -d "$media_database_name" -Atqc "SELECT count(*) FROM media_objects WHERE status NOT IN ('protected','deleted')")"
      pending_jobs="$("${DOCKER[@]}" compose exec -T postgres psql -v ON_ERROR_STOP=1 -U "$postgres_user" -d "$media_database_name" -Atqc "SELECT count(*) FROM media_outbox WHERE status IN ('pending','processing','retry')")"
      [[ "$pending_objects" == "0" ]] || die "Data Centre media has objects outside protected/deleted states; reconcile media before backup"
      [[ "$pending_jobs" == "0" ]] || die "Data Centre media outbox is not drained; retry after delivery/replication completes"
    }
    while IFS= read -r database_name; do
      if [[ "$media_external_enabled" == "true" && "$database_name" == "$media_database_name" ]]; then
        capture_media_snapshot "$provider_inventory_dir/media-before-dump"
      fi
      dump_path="$stage/postgres-$database_name.dump"
      "${DOCKER[@]}" compose exec -T postgres pg_dump -Fc --no-owner --no-acl -U "$postgres_user" -d "$database_name" > "$dump_path"
      [[ -s "$dump_path" ]] || die "PostgreSQL dump for $database_name is empty"
      "${DOCKER[@]}" compose exec -T postgres pg_restore --list < "$dump_path" > "$dump_path.list"
      [[ -s "$dump_path.list" ]] || die "PostgreSQL dump for $database_name failed archive-list validation"
      chmod 600 "$dump_path" "$dump_path.list"
      if [[ "$media_external_enabled" == "true" && "$database_name" == "$media_database_name" ]]; then
        capture_media_snapshot "$provider_inventory_dir/media-after-dump"
        cmp -s "$provider_inventory_dir/media-before-dump.objects" "$provider_inventory_dir/media-after-dump.objects" || die "Data Centre media changed during its PostgreSQL dump; retry the coordinated backup"
        cmp -s "$provider_inventory_dir/media-before-dump.outbox" "$provider_inventory_dir/media-after-dump.outbox" || die "Data Centre media outbox changed during its PostgreSQL dump; retry the coordinated backup"
        cp "$provider_inventory_dir/media-after-dump.objects" "$stage/media-objects.tsv"
      fi
    done < "$stage/postgres-databases.txt"

    if [[ -n "$video_media_source" ]]; then
      printf 'Copying Data Centre video media into protected staging...\n'
      cp -a -- "$video_media_source" "$stage/data-centre-video-media"
    fi
    if [[ -n "$node_storage_source" ]]; then
      printf 'Copying Data Centre node storage into protected staging...\n'
      cp -a -- "$node_storage_source" "$stage/data-centre-node-storage"
    fi

    printf 'Mirroring all MinIO buckets to protected staging...\n'
    mkdir -p "$stage/minio-private" "$stage/minio-public" "$stage/minio-identity-evidence" "$stage/minio-metadata"
    chmod 700 "$stage/minio-private" "$stage/minio-public" "$stage/minio-identity-evidence" "$stage/minio-metadata"
    # shellcheck disable=SC2016
    "${DOCKER[@]}" compose run --rm --no-deps \
      -v "$stage:/backup" \
      --entrypoint /bin/sh object-init -ec '
        mc alias set local http://object-storage:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null
        mc mirror --preserve local/"$MINIO_BUCKET" /backup/minio-private >/dev/null
        mc mirror --preserve local/"$PUBLIC_MEDIA_BUCKET" /backup/minio-public >/dev/null
        mc mirror --preserve local/"$IDENTITY_EVIDENCE_PRIVATE_BUCKET" /backup/minio-identity-evidence >/dev/null
        mc mirror --preserve local/"$MINIO_METADATA_BUCKET" /backup/minio-metadata >/dev/null
      '
    printf '%s\n' \
      "Testagram self-hosted backup" \
      "created_utc=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      "postgres_user=$postgres_user" \
      "postgres_primary_database=$postgres_primary_db" \
      "postgres_database_count=$(wc -l < "$stage/postgres-databases.txt" | tr -d ' ')" \
      "postgres_source_bytes=$postgres_bytes" \
      "private_bucket=${MINIO_BUCKET:-testagram-private-media}" \
      "public_bucket=${PUBLIC_MEDIA_BUCKET:-testagram-public-media}" \
      "identity_evidence_bucket=${IDENTITY_EVIDENCE_PRIVATE_BUCKET:-testagram-identity-evidence-private}" \
      "metadata_bucket=${MINIO_METADATA_BUCKET:-testagram-media-metadata}" \
      "data_centre_video_media_included=$([[ -n "$video_media_source" ]] && echo true || echo false)" \
      "data_centre_video_media_source=$video_media_source" \
      "data_centre_video_media_bytes=$video_media_bytes" \
      "data_centre_node_storage_included=$([[ -n "$node_storage_source" ]] && echo true || echo false)" \
      "data_centre_node_storage_source=$node_storage_source" \
      "data_centre_node_storage_bytes=$node_storage_bytes" \
      "repository=${RESTIC_REPOSITORY}" > "$stage/manifest.txt"
    chmod 600 "$stage/manifest.txt"
    printf 'Writing encrypted restic snapshot...\n'
    restic backup --host testagram-self-hosted --tag testagram --tag automated "$stage"
    printf 'Verifying repository metadata and a data subset...\n'
    restic check --read-data-subset=1/20
    printf 'Backup complete. Temporary plaintext staging will now be removed.\n'
    ;;
  check)
    restic snapshots --host testagram-self-hosted --tag testagram
    restic check --read-data-subset=1/20
    ;;
  prune)
    restic forget --host testagram-self-hosted --tag testagram \
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
\\t' read -r federation_database_user federation_database_name <<< "$federation_identity"
fi
if [[ "$media_external_enabled" == "true" ]]; then
  media_identity="$(parse_postgres_identity "$media_database_url")" || die "Data Centre media database is not on the backed-up PostgreSQL service"
  IFS=
data_centre_media_ids="$("${DOCKER[@]}" ps -a --filter label=com.docker.compose.project=testagram-data-centre --filter label=com.docker.compose.service=media --format '{{.ID}}')"
media_database_url=""
media_database_name=""
media_database_user=""
media_b2_endpoint=""
media_b2_region=""
media_b2_bucket=""
media_b2_access=""
media_b2_secret=""
media_r2_endpoint=""
media_r2_region=""
media_r2_bucket=""
media_r2_access=""
media_r2_secret=""
media_r2_metadata_bucket=""
media_vps_metadata_bucket=""
if [[ -n "$data_centre_media_ids" ]]; then
  [[ "$(wc -l <<< "$data_centre_media_ids" | tr -d ' ')" == "1" ]] || die "multiple Data Centre media containers found"
  media_container_id="$(head -n 1 <<< "$data_centre_media_ids")"
  media_running="$("${DOCKER[@]}" inspect "$media_container_id" | jq -er '.[0].State.Running')"
  [[ "$media_running" == "true" ]] || die "Data Centre media container exists but is not running; refusing an incomplete media backup"
  [[ "$(read_env_flag FRESH_MEDIA_ENABLED)" == "true" || "$(read_env_flag FRESH_MEDIA_ENABLED)" == "1" ]] || die "Data Centre media container is running while FRESH_MEDIA_ENABLED is not true"
  [[ "$(read_env_value DATA_CENTRE_MEDIA_API_URL)" == "http://media:8096" ]] || die "Backend is not configured for the active private Data Centre media service"
  media_database_url="$(container_env "$media_container_id" MEDIA_DATABASE_URL)" || die "Data Centre media container has no readable MEDIA_DATABASE_URL"
  media_b2_endpoint="$(container_env "$media_container_id" B2_S3_ENDPOINT)" || true
  media_b2_region="$(container_env "$media_container_id" B2_S3_REGION)" || true
  media_b2_bucket="$(container_env "$media_container_id" B2_S3_BUCKET)" || true
  media_b2_access="$(container_env "$media_container_id" B2_S3_ACCESS_KEY_ID)" || true
  media_b2_secret="$(container_env "$media_container_id" B2_S3_SECRET_ACCESS_KEY)" || true
  media_r2_endpoint="$(container_env "$media_container_id" CLOUDFLARE_R2_ENDPOINT)" || true
  media_r2_region="$(container_env "$media_container_id" CLOUDFLARE_R2_REGION)" || true
  media_r2_bucket="$(container_env "$media_container_id" CLOUDFLARE_R2_BUCKET)" || true
  media_r2_access="$(container_env "$media_container_id" CLOUDFLARE_R2_ACCESS_KEY_ID)" || true
  media_r2_secret="$(container_env "$media_container_id" CLOUDFLARE_R2_SECRET_ACCESS_KEY)" || true
  media_r2_metadata_bucket="$(container_env "$media_container_id" CLOUDFLARE_R2_METADATA_BUCKET)" || true
  media_vps_metadata_bucket="$(container_env "$media_container_id" VPS_MINIO_METADATA_BUCKET)" || true
  for value in "$media_b2_endpoint" "$media_b2_region" "$media_b2_bucket" "$media_b2_access" "$media_b2_secret" "$media_r2_endpoint" "$media_r2_region" "$media_r2_bucket" "$media_r2_access" "$media_r2_secret" "$media_r2_metadata_bucket" "$media_vps_metadata_bucket"; do
    [[ -n "$value" ]] || die "Data Centre media provider configuration is incomplete; refusing to omit external objects"
  done
  media_external_enabled=true
elif [[ "$(read_env_flag FRESH_MEDIA_ENABLED)" == "true" || "$(read_env_flag FRESH_MEDIA_ENABLED)" == "1" || -n "$(read_env_value DATA_CENTRE_MEDIA_API_URL)" ]]; then
  die "Data Centre media is configured but its runtime container is missing; refusing to omit external objects"
fi

federation_db_ids="$("${DOCKER[@]}" ps -a --filter label=com.docker.compose.service=federation-db --format '{{.ID}}')"
[[ -z "$federation_db_ids" ]] || die "a separate federation-db container exists; back it up and restore-test it before relying on this backup"

video_media_source=""
for service_name in video video-worker; do
  service_ids="$("${DOCKER[@]}" ps -a --filter label=com.docker.compose.project=testagram-data-centre --filter "label=com.docker.compose.service=$service_name" --format '{{.ID}}')"
  if [[ -n "$service_ids" ]]; then
    [[ "$(wc -l <<< "$service_ids" | tr -d ' ')" == "1" ]] || die "multiple Data Centre $service_name containers found; resolve the storage mounts before backup"
    service_id="$(head -n 1 <<< "$service_ids")"
    current_source="$("${DOCKER[@]}" inspect "$service_id" | jq -er '.[0].Mounts[] | select(.Destination == "/media" and .Type == "bind") | .Source' 2>/dev/null || true)"
    [[ -n "$current_source" && -d "$current_source" ]] || die "Data Centre $service_name does not expose a readable bind mount at /media; refusing to omit its files"
    current_source="$(realpath "$current_source")"
    if [[ -n "$video_media_source" && "$video_media_source" != "$current_source" ]]; then
      die "Data Centre video and video-worker use different media sources; reconcile them before backup"
    fi
    video_media_source="$current_source"
  fi
done

storage_root="$(read_env_value TESTAGRAM_STORAGE_ROOT)"
[[ -n "$storage_root" ]] || storage_root="/var/lib/testagram/storage"
if [[ -z "$video_media_source" && -d "$storage_root/media" ]]; then
  video_media_source="$(realpath "$storage_root/media")"
fi
node_storage_source=""
storage_root_for_nodes="$storage_root"
if [[ ! -d "$storage_root_for_nodes/nodes" && -n "$video_media_source" ]]; then
  storage_root_for_nodes="$(dirname "$video_media_source")"
fi
if [[ -d "$storage_root_for_nodes/nodes" ]]; then
  node_storage_source="$(realpath "$storage_root_for_nodes/nodes")"
fi

"${DOCKER[@]}" compose config --quiet
    postgres_user="$("${DOCKER[@]}" compose exec -T postgres printenv POSTGRES_USER | tr -d '\r')"
    postgres_primary_db="$("${DOCKER[@]}" compose exec -T postgres printenv POSTGRES_DB | tr -d '\r')"
    [[ "$postgres_user" =~ ^[A-Za-z0-9_]+$ && "$postgres_primary_db" =~ ^[A-Za-z0-9_]+$ ]] || die "PostgreSQL user/database names are missing or unsupported"
    extra_postgres_roles="$("${DOCKER[@]}" compose exec -T postgres psql -U "$postgres_user" -d "$postgres_primary_db" -Atqc "SELECT rolname FROM pg_roles WHERE rolname !~ '^pg_' AND rolname NOT IN ('postgres', '$postgres_user') ORDER BY rolname")"
    [[ -z "$extra_postgres_roles" ]] || die "additional PostgreSQL roles detected ($extra_postgres_roles); add a tested globals backup/restore before relying on this backup"
    extra_tablespaces="$("${DOCKER[@]}" compose exec -T postgres psql -U "$postgres_user" -d "$postgres_primary_db" -Atqc "SELECT spcname FROM pg_tablespace WHERE spcname NOT IN ('pg_default', 'pg_global') ORDER BY spcname")"
    [[ -z "$extra_tablespaces" ]] || die "user-defined PostgreSQL tablespaces detected ($extra_tablespaces); back up their external paths before relying on this workflow"
    postgres_database_names="$("${DOCKER[@]}" compose exec -T postgres psql -U "$postgres_user" -d "$postgres_primary_db" -Atqc "SELECT datname FROM pg_database WHERE datallowconn AND NOT datistemplate ORDER BY datname")"
    [[ -n "$postgres_database_names" ]] || die "PostgreSQL returned no connectable databases"
    grep -Fxq "$postgres_primary_db" <<< "$postgres_database_names" || die "primary PostgreSQL database is absent from the database inventory"
    if [[ -n "$federation_database_name" ]]; then
      grep -Fxq "$federation_database_name" <<< "$postgres_database_names" || die "Federation API points to a database that is not present in the backed-up PostgreSQL cluster"
    fi
    while IFS= read -r database_name; do
      [[ "$database_name" =~ ^[A-Za-z0-9_]+$ ]] || die "unsupported PostgreSQL database name discovered; refusing unsafe dump path"
    done <<< "$postgres_database_names"
    stage_parent="${BACKUP_STAGE_DIR:-${TMPDIR:-/tmp}}"
    mkdir -p "$stage_parent"
    stage_parent_real="$(realpath "$stage_parent")"
    for source_path in "$video_media_source" "$node_storage_source"; do
      [[ -n "$source_path" ]] || continue
      [[ "$source_path" != "/" ]] || die "refusing to back up the entire filesystem as Data Centre media"
      case "$stage_parent_real/" in
        "$source_path/"*) die "backup staging directory is inside a Data Centre source directory" ;;
      esac
      case "$source_path/" in
        "$stage_parent_real/"*) die "Data Centre source directory is inside the backup staging directory" ;;
      esac
    done

    printf 'Estimating PostgreSQL, MinIO and detected Data Centre files before staging...\n'
    # shellcheck disable=SC2016
    bucket_size_report="$("${DOCKER[@]}" compose run --rm --no-deps \
      --entrypoint /bin/sh object-init -ec '
        mc alias set local http://object-storage:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null
        mc stat "local/$MINIO_BUCKET" --json
        mc stat "local/$PUBLIC_MEDIA_BUCKET" --json
        mc stat "local/$IDENTITY_EVIDENCE_PRIVATE_BUCKET" --json
        mc stat "local/$MINIO_METADATA_BUCKET" --json
      ')"
    minio_bytes="$(jq -s -er 'if length == 4 and all(.[]; (.Usage.totalSize | type) == "number") then map(.Usage.totalSize) | add else error("MinIO did not report sizes for all four buckets") end' <<< "$bucket_size_report")"
    postgres_bytes="$("${DOCKER[@]}" compose exec -T postgres psql -U "$postgres_user" -d "$postgres_primary_db" -Atqc "SELECT COALESCE(SUM(pg_database_size(datname)),0) FROM pg_database WHERE datallowconn AND NOT datistemplate")"
    video_media_bytes=0
    node_storage_bytes=0
    if [[ -n "$video_media_source" ]]; then
      video_media_bytes="$(du -sb "$video_media_source" | awk '{print $1}')"
    fi
    if [[ -n "$node_storage_source" ]]; then
      node_storage_bytes="$(du -sb "$node_storage_source" | awk '{print $1}')"
    fi
    [[ "$minio_bytes" =~ ^[0-9]+$ && "$postgres_bytes" =~ ^[0-9]+$ && "$video_media_bytes" =~ ^[0-9]+$ && "$node_storage_bytes" =~ ^[0-9]+$ ]] || die "could not determine database, MinIO or Data Centre storage sizes"
    required_stage_bytes=$(( (minio_bytes + postgres_bytes + video_media_bytes + node_storage_bytes) * 120 / 100 + 1073741824 ))
    available_stage_bytes="$(df -PB1 "$stage_parent" | awk 'NR == 2 { print $4 }')"
    docker_root="$("${DOCKER[@]}" info --format '{{.DockerRootDir}}')"
    available_docker_bytes="$(df -PB1 "$docker_root" | awk 'NR == 2 { print $4 }')"
    [[ "$available_stage_bytes" =~ ^[0-9]+$ && "$available_docker_bytes" =~ ^[0-9]+$ ]] || die "could not determine free disk space"
    printf 'Estimated backup source: PostgreSQL=%s bytes, MinIO=%s bytes, Data Centre video=%s bytes, node storage=%s bytes; staging free=%s bytes.\n' \
      "$postgres_bytes" "$minio_bytes" "$video_media_bytes" "$node_storage_bytes" "$available_stage_bytes"
    (( available_stage_bytes >= required_stage_bytes )) || die "insufficient staging space; need at least ${required_stage_bytes} bytes free for a safe full copy"
    (( available_docker_bytes >= 1073741824 )) || die "less than 1 GiB remains in Docker storage; refusing to risk production containers"

    stage=$(mktemp -d "${stage_parent%/}/testagram-backup.XXXXXXXX")
    chmod 700 "$stage"
    cleanup() { rm -rf -- "$stage"; }
    trap cleanup EXIT INT TERM

    printf '%s\n' "$postgres_database_names" > "$stage/postgres-databases.txt"
    printf 'Exporting %s PostgreSQL databases...\n' "$(wc -l < "$stage/postgres-databases.txt" | tr -d ' ')"
    while IFS= read -r database_name; do
      dump_path="$stage/postgres-$database_name.dump"
      "${DOCKER[@]}" compose exec -T postgres pg_dump -Fc --no-owner --no-acl -U "$postgres_user" -d "$database_name" > "$dump_path"
      [[ -s "$dump_path" ]] || die "PostgreSQL dump for $database_name is empty"
      "${DOCKER[@]}" compose exec -T postgres pg_restore --list < "$dump_path" > "$dump_path.list"
      [[ -s "$dump_path.list" ]] || die "PostgreSQL dump for $database_name failed archive-list validation"
      chmod 600 "$dump_path" "$dump_path.list"
    done < "$stage/postgres-databases.txt"

    if [[ -n "$video_media_source" ]]; then
      printf 'Copying Data Centre video media into protected staging...\n'
      cp -a -- "$video_media_source" "$stage/data-centre-video-media"
    fi
    if [[ -n "$node_storage_source" ]]; then
      printf 'Copying Data Centre node storage into protected staging...\n'
      cp -a -- "$node_storage_source" "$stage/data-centre-node-storage"
    fi

    printf 'Mirroring all MinIO buckets to protected staging...\n'
    mkdir -p "$stage/minio-private" "$stage/minio-public" "$stage/minio-identity-evidence" "$stage/minio-metadata"
    chmod 700 "$stage/minio-private" "$stage/minio-public" "$stage/minio-identity-evidence" "$stage/minio-metadata"
    # shellcheck disable=SC2016
    "${DOCKER[@]}" compose run --rm --no-deps \
      -v "$stage:/backup" \
      --entrypoint /bin/sh object-init -ec '
        mc alias set local http://object-storage:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null
        mc mirror --preserve local/"$MINIO_BUCKET" /backup/minio-private >/dev/null
        mc mirror --preserve local/"$PUBLIC_MEDIA_BUCKET" /backup/minio-public >/dev/null
        mc mirror --preserve local/"$IDENTITY_EVIDENCE_PRIVATE_BUCKET" /backup/minio-identity-evidence >/dev/null
        mc mirror --preserve local/"$MINIO_METADATA_BUCKET" /backup/minio-metadata >/dev/null
      '
    printf '%s\n' \
      "Testagram self-hosted backup" \
      "created_utc=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      "postgres_user=$postgres_user" \
      "postgres_primary_database=$postgres_primary_db" \
      "postgres_database_count=$(wc -l < "$stage/postgres-databases.txt" | tr -d ' ')" \
      "postgres_source_bytes=$postgres_bytes" \
      "private_bucket=${MINIO_BUCKET:-testagram-private-media}" \
      "public_bucket=${PUBLIC_MEDIA_BUCKET:-testagram-public-media}" \
      "identity_evidence_bucket=${IDENTITY_EVIDENCE_PRIVATE_BUCKET:-testagram-identity-evidence-private}" \
      "metadata_bucket=${MINIO_METADATA_BUCKET:-testagram-media-metadata}" \
      "data_centre_video_media_included=$([[ -n "$video_media_source" ]] && echo true || echo false)" \
      "data_centre_video_media_source=$video_media_source" \
      "data_centre_video_media_bytes=$video_media_bytes" \
      "data_centre_node_storage_included=$([[ -n "$node_storage_source" ]] && echo true || echo false)" \
      "data_centre_node_storage_source=$node_storage_source" \
      "data_centre_node_storage_bytes=$node_storage_bytes" \
      "repository=${RESTIC_REPOSITORY}" > "$stage/manifest.txt"
    chmod 600 "$stage/manifest.txt"
    printf 'Writing encrypted restic snapshot...\n'
    restic backup --host testagram-self-hosted --tag testagram --tag automated "$stage"
    printf 'Verifying repository metadata and a data subset...\n'
    restic check --read-data-subset=1/20
    printf 'Backup complete. Temporary plaintext staging will now be removed.\n'
    ;;
  check)
    restic snapshots --host testagram-self-hosted --tag testagram
    restic check --read-data-subset=1/20
    ;;
  prune)
    restic forget --host testagram-self-hosted --tag testagram \
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
\\t' read -r media_database_user media_database_name <<< "$media_identity"
  [[ "$media_vps_metadata_bucket" == "$(read_env_value MINIO_METADATA_BUCKET)" ]] || die "Data Centre metadata primary bucket differs from the local MinIO bucket included in this backup"
  backup_identity="$(python3 - "$RESTIC_REPOSITORY" <<'PY'
import sys
from urllib.parse import urlparse
value = sys.argv[1]
if not value.startswith("s3:"):
    sys.exit(1)
parsed = urlparse(value[3:])
parts = [part for part in parsed.path.split("/") if part]
if parsed.scheme != "https" or not parsed.hostname or not parts:
    sys.exit(1)
print(parsed.hostname.lower() + "|" + parts[0])
PY
  )" || die "Restic destination must be an S3 HTTPS repository with a bucket path"
  IFS=
data_centre_media_ids="$("${DOCKER[@]}" ps -a --filter label=com.docker.compose.project=testagram-data-centre --filter label=com.docker.compose.service=media --format '{{.ID}}')"
media_database_url=""
media_database_name=""
media_database_user=""
media_b2_endpoint=""
media_b2_region=""
media_b2_bucket=""
media_b2_access=""
media_b2_secret=""
media_r2_endpoint=""
media_r2_region=""
media_r2_bucket=""
media_r2_access=""
media_r2_secret=""
media_r2_metadata_bucket=""
media_vps_metadata_bucket=""
if [[ -n "$data_centre_media_ids" ]]; then
  [[ "$(wc -l <<< "$data_centre_media_ids" | tr -d ' ')" == "1" ]] || die "multiple Data Centre media containers found"
  media_container_id="$(head -n 1 <<< "$data_centre_media_ids")"
  media_running="$("${DOCKER[@]}" inspect "$media_container_id" | jq -er '.[0].State.Running')"
  [[ "$media_running" == "true" ]] || die "Data Centre media container exists but is not running; refusing an incomplete media backup"
  [[ "$(read_env_flag FRESH_MEDIA_ENABLED)" == "true" || "$(read_env_flag FRESH_MEDIA_ENABLED)" == "1" ]] || die "Data Centre media container is running while FRESH_MEDIA_ENABLED is not true"
  [[ "$(read_env_value DATA_CENTRE_MEDIA_API_URL)" == "http://media:8096" ]] || die "Backend is not configured for the active private Data Centre media service"
  media_database_url="$(container_env "$media_container_id" MEDIA_DATABASE_URL)" || die "Data Centre media container has no readable MEDIA_DATABASE_URL"
  media_b2_endpoint="$(container_env "$media_container_id" B2_S3_ENDPOINT)" || true
  media_b2_region="$(container_env "$media_container_id" B2_S3_REGION)" || true
  media_b2_bucket="$(container_env "$media_container_id" B2_S3_BUCKET)" || true
  media_b2_access="$(container_env "$media_container_id" B2_S3_ACCESS_KEY_ID)" || true
  media_b2_secret="$(container_env "$media_container_id" B2_S3_SECRET_ACCESS_KEY)" || true
  media_r2_endpoint="$(container_env "$media_container_id" CLOUDFLARE_R2_ENDPOINT)" || true
  media_r2_region="$(container_env "$media_container_id" CLOUDFLARE_R2_REGION)" || true
  media_r2_bucket="$(container_env "$media_container_id" CLOUDFLARE_R2_BUCKET)" || true
  media_r2_access="$(container_env "$media_container_id" CLOUDFLARE_R2_ACCESS_KEY_ID)" || true
  media_r2_secret="$(container_env "$media_container_id" CLOUDFLARE_R2_SECRET_ACCESS_KEY)" || true
  media_r2_metadata_bucket="$(container_env "$media_container_id" CLOUDFLARE_R2_METADATA_BUCKET)" || true
  media_vps_metadata_bucket="$(container_env "$media_container_id" VPS_MINIO_METADATA_BUCKET)" || true
  for value in "$media_b2_endpoint" "$media_b2_region" "$media_b2_bucket" "$media_b2_access" "$media_b2_secret" "$media_r2_endpoint" "$media_r2_region" "$media_r2_bucket" "$media_r2_access" "$media_r2_secret" "$media_r2_metadata_bucket" "$media_vps_metadata_bucket"; do
    [[ -n "$value" ]] || die "Data Centre media provider configuration is incomplete; refusing to omit external objects"
  done
  media_external_enabled=true
elif [[ "$(read_env_flag FRESH_MEDIA_ENABLED)" == "true" || "$(read_env_flag FRESH_MEDIA_ENABLED)" == "1" || -n "$(read_env_value DATA_CENTRE_MEDIA_API_URL)" ]]; then
  die "Data Centre media is configured but its runtime container is missing; refusing to omit external objects"
fi

federation_db_ids="$("${DOCKER[@]}" ps -a --filter label=com.docker.compose.service=federation-db --format '{{.ID}}')"
[[ -z "$federation_db_ids" ]] || die "a separate federation-db container exists; back it up and restore-test it before relying on this backup"

video_media_source=""
for service_name in video video-worker; do
  service_ids="$("${DOCKER[@]}" ps -a --filter label=com.docker.compose.project=testagram-data-centre --filter "label=com.docker.compose.service=$service_name" --format '{{.ID}}')"
  if [[ -n "$service_ids" ]]; then
    [[ "$(wc -l <<< "$service_ids" | tr -d ' ')" == "1" ]] || die "multiple Data Centre $service_name containers found; resolve the storage mounts before backup"
    service_id="$(head -n 1 <<< "$service_ids")"
    current_source="$("${DOCKER[@]}" inspect "$service_id" | jq -er '.[0].Mounts[] | select(.Destination == "/media" and .Type == "bind") | .Source' 2>/dev/null || true)"
    [[ -n "$current_source" && -d "$current_source" ]] || die "Data Centre $service_name does not expose a readable bind mount at /media; refusing to omit its files"
    current_source="$(realpath "$current_source")"
    if [[ -n "$video_media_source" && "$video_media_source" != "$current_source" ]]; then
      die "Data Centre video and video-worker use different media sources; reconcile them before backup"
    fi
    video_media_source="$current_source"
  fi
done

storage_root="$(read_env_value TESTAGRAM_STORAGE_ROOT)"
[[ -n "$storage_root" ]] || storage_root="/var/lib/testagram/storage"
if [[ -z "$video_media_source" && -d "$storage_root/media" ]]; then
  video_media_source="$(realpath "$storage_root/media")"
fi
node_storage_source=""
storage_root_for_nodes="$storage_root"
if [[ ! -d "$storage_root_for_nodes/nodes" && -n "$video_media_source" ]]; then
  storage_root_for_nodes="$(dirname "$video_media_source")"
fi
if [[ -d "$storage_root_for_nodes/nodes" ]]; then
  node_storage_source="$(realpath "$storage_root_for_nodes/nodes")"
fi

"${DOCKER[@]}" compose config --quiet
    postgres_user="$("${DOCKER[@]}" compose exec -T postgres printenv POSTGRES_USER | tr -d '\r')"
    postgres_primary_db="$("${DOCKER[@]}" compose exec -T postgres printenv POSTGRES_DB | tr -d '\r')"
    [[ "$postgres_user" =~ ^[A-Za-z0-9_]+$ && "$postgres_primary_db" =~ ^[A-Za-z0-9_]+$ ]] || die "PostgreSQL user/database names are missing or unsupported"
    extra_postgres_roles="$("${DOCKER[@]}" compose exec -T postgres psql -U "$postgres_user" -d "$postgres_primary_db" -Atqc "SELECT rolname FROM pg_roles WHERE rolname !~ '^pg_' AND rolname NOT IN ('postgres', '$postgres_user') ORDER BY rolname")"
    [[ -z "$extra_postgres_roles" ]] || die "additional PostgreSQL roles detected ($extra_postgres_roles); add a tested globals backup/restore before relying on this backup"
    extra_tablespaces="$("${DOCKER[@]}" compose exec -T postgres psql -U "$postgres_user" -d "$postgres_primary_db" -Atqc "SELECT spcname FROM pg_tablespace WHERE spcname NOT IN ('pg_default', 'pg_global') ORDER BY spcname")"
    [[ -z "$extra_tablespaces" ]] || die "user-defined PostgreSQL tablespaces detected ($extra_tablespaces); back up their external paths before relying on this workflow"
    postgres_database_names="$("${DOCKER[@]}" compose exec -T postgres psql -U "$postgres_user" -d "$postgres_primary_db" -Atqc "SELECT datname FROM pg_database WHERE datallowconn AND NOT datistemplate ORDER BY datname")"
    [[ -n "$postgres_database_names" ]] || die "PostgreSQL returned no connectable databases"
    grep -Fxq "$postgres_primary_db" <<< "$postgres_database_names" || die "primary PostgreSQL database is absent from the database inventory"
    if [[ -n "$federation_database_name" ]]; then
      grep -Fxq "$federation_database_name" <<< "$postgres_database_names" || die "Federation API points to a database that is not present in the backed-up PostgreSQL cluster"
    fi
    while IFS= read -r database_name; do
      [[ "$database_name" =~ ^[A-Za-z0-9_]+$ ]] || die "unsupported PostgreSQL database name discovered; refusing unsafe dump path"
    done <<< "$postgres_database_names"
    stage_parent="${BACKUP_STAGE_DIR:-${TMPDIR:-/tmp}}"
    mkdir -p "$stage_parent"
    stage_parent_real="$(realpath "$stage_parent")"
    for source_path in "$video_media_source" "$node_storage_source"; do
      [[ -n "$source_path" ]] || continue
      [[ "$source_path" != "/" ]] || die "refusing to back up the entire filesystem as Data Centre media"
      case "$stage_parent_real/" in
        "$source_path/"*) die "backup staging directory is inside a Data Centre source directory" ;;
      esac
      case "$source_path/" in
        "$stage_parent_real/"*) die "Data Centre source directory is inside the backup staging directory" ;;
      esac
    done

    printf 'Estimating PostgreSQL, MinIO and detected Data Centre files before staging...\n'
    # shellcheck disable=SC2016
    bucket_size_report="$("${DOCKER[@]}" compose run --rm --no-deps \
      --entrypoint /bin/sh object-init -ec '
        mc alias set local http://object-storage:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null
        mc stat "local/$MINIO_BUCKET" --json
        mc stat "local/$PUBLIC_MEDIA_BUCKET" --json
        mc stat "local/$IDENTITY_EVIDENCE_PRIVATE_BUCKET" --json
        mc stat "local/$MINIO_METADATA_BUCKET" --json
      ')"
    minio_bytes="$(jq -s -er 'if length == 4 and all(.[]; (.Usage.totalSize | type) == "number") then map(.Usage.totalSize) | add else error("MinIO did not report sizes for all four buckets") end' <<< "$bucket_size_report")"
    postgres_bytes="$("${DOCKER[@]}" compose exec -T postgres psql -U "$postgres_user" -d "$postgres_primary_db" -Atqc "SELECT COALESCE(SUM(pg_database_size(datname)),0) FROM pg_database WHERE datallowconn AND NOT datistemplate")"
    video_media_bytes=0
    node_storage_bytes=0
    if [[ -n "$video_media_source" ]]; then
      video_media_bytes="$(du -sb "$video_media_source" | awk '{print $1}')"
    fi
    if [[ -n "$node_storage_source" ]]; then
      node_storage_bytes="$(du -sb "$node_storage_source" | awk '{print $1}')"
    fi
    [[ "$minio_bytes" =~ ^[0-9]+$ && "$postgres_bytes" =~ ^[0-9]+$ && "$video_media_bytes" =~ ^[0-9]+$ && "$node_storage_bytes" =~ ^[0-9]+$ ]] || die "could not determine database, MinIO or Data Centre storage sizes"
    required_stage_bytes=$(( (minio_bytes + postgres_bytes + video_media_bytes + node_storage_bytes) * 120 / 100 + 1073741824 ))
    available_stage_bytes="$(df -PB1 "$stage_parent" | awk 'NR == 2 { print $4 }')"
    docker_root="$("${DOCKER[@]}" info --format '{{.DockerRootDir}}')"
    available_docker_bytes="$(df -PB1 "$docker_root" | awk 'NR == 2 { print $4 }')"
    [[ "$available_stage_bytes" =~ ^[0-9]+$ && "$available_docker_bytes" =~ ^[0-9]+$ ]] || die "could not determine free disk space"
    printf 'Estimated backup source: PostgreSQL=%s bytes, MinIO=%s bytes, Data Centre video=%s bytes, node storage=%s bytes; staging free=%s bytes.\n' \
      "$postgres_bytes" "$minio_bytes" "$video_media_bytes" "$node_storage_bytes" "$available_stage_bytes"
    (( available_stage_bytes >= required_stage_bytes )) || die "insufficient staging space; need at least ${required_stage_bytes} bytes free for a safe full copy"
    (( available_docker_bytes >= 1073741824 )) || die "less than 1 GiB remains in Docker storage; refusing to risk production containers"

    stage=$(mktemp -d "${stage_parent%/}/testagram-backup.XXXXXXXX")
    chmod 700 "$stage"
    cleanup() { rm -rf -- "$stage"; }
    trap cleanup EXIT INT TERM

    printf '%s\n' "$postgres_database_names" > "$stage/postgres-databases.txt"
    printf 'Exporting %s PostgreSQL databases...\n' "$(wc -l < "$stage/postgres-databases.txt" | tr -d ' ')"
    while IFS= read -r database_name; do
      dump_path="$stage/postgres-$database_name.dump"
      "${DOCKER[@]}" compose exec -T postgres pg_dump -Fc --no-owner --no-acl -U "$postgres_user" -d "$database_name" > "$dump_path"
      [[ -s "$dump_path" ]] || die "PostgreSQL dump for $database_name is empty"
      "${DOCKER[@]}" compose exec -T postgres pg_restore --list < "$dump_path" > "$dump_path.list"
      [[ -s "$dump_path.list" ]] || die "PostgreSQL dump for $database_name failed archive-list validation"
      chmod 600 "$dump_path" "$dump_path.list"
    done < "$stage/postgres-databases.txt"

    if [[ -n "$video_media_source" ]]; then
      printf 'Copying Data Centre video media into protected staging...\n'
      cp -a -- "$video_media_source" "$stage/data-centre-video-media"
    fi
    if [[ -n "$node_storage_source" ]]; then
      printf 'Copying Data Centre node storage into protected staging...\n'
      cp -a -- "$node_storage_source" "$stage/data-centre-node-storage"
    fi

    printf 'Mirroring all MinIO buckets to protected staging...\n'
    mkdir -p "$stage/minio-private" "$stage/minio-public" "$stage/minio-identity-evidence" "$stage/minio-metadata"
    chmod 700 "$stage/minio-private" "$stage/minio-public" "$stage/minio-identity-evidence" "$stage/minio-metadata"
    # shellcheck disable=SC2016
    "${DOCKER[@]}" compose run --rm --no-deps \
      -v "$stage:/backup" \
      --entrypoint /bin/sh object-init -ec '
        mc alias set local http://object-storage:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null
        mc mirror --preserve local/"$MINIO_BUCKET" /backup/minio-private >/dev/null
        mc mirror --preserve local/"$PUBLIC_MEDIA_BUCKET" /backup/minio-public >/dev/null
        mc mirror --preserve local/"$IDENTITY_EVIDENCE_PRIVATE_BUCKET" /backup/minio-identity-evidence >/dev/null
        mc mirror --preserve local/"$MINIO_METADATA_BUCKET" /backup/minio-metadata >/dev/null
      '
    printf '%s\n' \
      "Testagram self-hosted backup" \
      "created_utc=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      "postgres_user=$postgres_user" \
      "postgres_primary_database=$postgres_primary_db" \
      "postgres_database_count=$(wc -l < "$stage/postgres-databases.txt" | tr -d ' ')" \
      "postgres_source_bytes=$postgres_bytes" \
      "private_bucket=${MINIO_BUCKET:-testagram-private-media}" \
      "public_bucket=${PUBLIC_MEDIA_BUCKET:-testagram-public-media}" \
      "identity_evidence_bucket=${IDENTITY_EVIDENCE_PRIVATE_BUCKET:-testagram-identity-evidence-private}" \
      "metadata_bucket=${MINIO_METADATA_BUCKET:-testagram-media-metadata}" \
      "data_centre_video_media_included=$([[ -n "$video_media_source" ]] && echo true || echo false)" \
      "data_centre_video_media_source=$video_media_source" \
      "data_centre_video_media_bytes=$video_media_bytes" \
      "data_centre_node_storage_included=$([[ -n "$node_storage_source" ]] && echo true || echo false)" \
      "data_centre_node_storage_source=$node_storage_source" \
      "data_centre_node_storage_bytes=$node_storage_bytes" \
      "repository=${RESTIC_REPOSITORY}" > "$stage/manifest.txt"
    chmod 600 "$stage/manifest.txt"
    printf 'Writing encrypted restic snapshot...\n'
    restic backup --host testagram-self-hosted --tag testagram --tag automated "$stage"
    printf 'Verifying repository metadata and a data subset...\n'
    restic check --read-data-subset=1/20
    printf 'Backup complete. Temporary plaintext staging will now be removed.\n'
    ;;
  check)
    restic snapshots --host testagram-self-hosted --tag testagram
    restic check --read-data-subset=1/20
    ;;
  prune)
    restic forget --host testagram-self-hosted --tag testagram \
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
\\t' read -r backup_host backup_bucket <<< "$backup_identity"
  for source in "$media_b2_endpoint|$media_b2_bucket" "$media_r2_endpoint|$media_r2_bucket" "$media_r2_endpoint|$media_r2_metadata_bucket"; do
    source_host="${source%%|*}"
    source_bucket="${source#*|}"
    source_host="$(python3 - "$source_host" <<'PY'
import sys
from urllib.parse import urlparse
print((urlparse(sys.argv[1]).hostname or "").lower())
PY
    )"
    [[ "$backup_host" != "$source_host" || "$backup_bucket" != "$source_bucket" ]] || die "Restic backup destination must be a separate bucket from active B2/R2 media storage"
  done
fi

data_centre_media_ids="$("${DOCKER[@]}" ps -a --filter label=com.docker.compose.project=testagram-data-centre --filter label=com.docker.compose.service=media --format '{{.ID}}')"
media_database_url=""
media_database_name=""
media_database_user=""
media_b2_endpoint=""
media_b2_region=""
media_b2_bucket=""
media_b2_access=""
media_b2_secret=""
media_r2_endpoint=""
media_r2_region=""
media_r2_bucket=""
media_r2_access=""
media_r2_secret=""
media_r2_metadata_bucket=""
media_vps_metadata_bucket=""
if [[ -n "$data_centre_media_ids" ]]; then
  [[ "$(wc -l <<< "$data_centre_media_ids" | tr -d ' ')" == "1" ]] || die "multiple Data Centre media containers found"
  media_container_id="$(head -n 1 <<< "$data_centre_media_ids")"
  media_running="$("${DOCKER[@]}" inspect "$media_container_id" | jq -er '.[0].State.Running')"
  [[ "$media_running" == "true" ]] || die "Data Centre media container exists but is not running; refusing an incomplete media backup"
  [[ "$(read_env_flag FRESH_MEDIA_ENABLED)" == "true" || "$(read_env_flag FRESH_MEDIA_ENABLED)" == "1" ]] || die "Data Centre media container is running while FRESH_MEDIA_ENABLED is not true"
  [[ "$(read_env_value DATA_CENTRE_MEDIA_API_URL)" == "http://media:8096" ]] || die "Backend is not configured for the active private Data Centre media service"
  media_database_url="$(container_env "$media_container_id" MEDIA_DATABASE_URL)" || die "Data Centre media container has no readable MEDIA_DATABASE_URL"
  media_b2_endpoint="$(container_env "$media_container_id" B2_S3_ENDPOINT)" || true
  media_b2_region="$(container_env "$media_container_id" B2_S3_REGION)" || true
  media_b2_bucket="$(container_env "$media_container_id" B2_S3_BUCKET)" || true
  media_b2_access="$(container_env "$media_container_id" B2_S3_ACCESS_KEY_ID)" || true
  media_b2_secret="$(container_env "$media_container_id" B2_S3_SECRET_ACCESS_KEY)" || true
  media_r2_endpoint="$(container_env "$media_container_id" CLOUDFLARE_R2_ENDPOINT)" || true
  media_r2_region="$(container_env "$media_container_id" CLOUDFLARE_R2_REGION)" || true
  media_r2_bucket="$(container_env "$media_container_id" CLOUDFLARE_R2_BUCKET)" || true
  media_r2_access="$(container_env "$media_container_id" CLOUDFLARE_R2_ACCESS_KEY_ID)" || true
  media_r2_secret="$(container_env "$media_container_id" CLOUDFLARE_R2_SECRET_ACCESS_KEY)" || true
  media_r2_metadata_bucket="$(container_env "$media_container_id" CLOUDFLARE_R2_METADATA_BUCKET)" || true
  media_vps_metadata_bucket="$(container_env "$media_container_id" VPS_MINIO_METADATA_BUCKET)" || true
  for value in "$media_b2_endpoint" "$media_b2_region" "$media_b2_bucket" "$media_b2_access" "$media_b2_secret" "$media_r2_endpoint" "$media_r2_region" "$media_r2_bucket" "$media_r2_access" "$media_r2_secret" "$media_r2_metadata_bucket" "$media_vps_metadata_bucket"; do
    [[ -n "$value" ]] || die "Data Centre media provider configuration is incomplete; refusing to omit external objects"
  done
  media_external_enabled=true
elif [[ "$(read_env_flag FRESH_MEDIA_ENABLED)" == "true" || "$(read_env_flag FRESH_MEDIA_ENABLED)" == "1" || -n "$(read_env_value DATA_CENTRE_MEDIA_API_URL)" ]]; then
  die "Data Centre media is configured but its runtime container is missing; refusing to omit external objects"
fi

federation_db_ids="$("${DOCKER[@]}" ps -a --filter label=com.docker.compose.service=federation-db --format '{{.ID}}')"
[[ -z "$federation_db_ids" ]] || die "a separate federation-db container exists; back it up and restore-test it before relying on this backup"

video_media_source=""
for service_name in video video-worker; do
  service_ids="$("${DOCKER[@]}" ps -a --filter label=com.docker.compose.project=testagram-data-centre --filter "label=com.docker.compose.service=$service_name" --format '{{.ID}}')"
  if [[ -n "$service_ids" ]]; then
    [[ "$(wc -l <<< "$service_ids" | tr -d ' ')" == "1" ]] || die "multiple Data Centre $service_name containers found; resolve the storage mounts before backup"
    service_id="$(head -n 1 <<< "$service_ids")"
    current_source="$("${DOCKER[@]}" inspect "$service_id" | jq -er '.[0].Mounts[] | select(.Destination == "/media" and .Type == "bind") | .Source' 2>/dev/null || true)"
    [[ -n "$current_source" && -d "$current_source" ]] || die "Data Centre $service_name does not expose a readable bind mount at /media; refusing to omit its files"
    current_source="$(realpath "$current_source")"
    if [[ -n "$video_media_source" && "$video_media_source" != "$current_source" ]]; then
      die "Data Centre video and video-worker use different media sources; reconcile them before backup"
    fi
    video_media_source="$current_source"
  fi
done

storage_root="$(read_env_value TESTAGRAM_STORAGE_ROOT)"
[[ -n "$storage_root" ]] || storage_root="/var/lib/testagram/storage"
if [[ -z "$video_media_source" && -d "$storage_root/media" ]]; then
  video_media_source="$(realpath "$storage_root/media")"
fi
node_storage_source=""
storage_root_for_nodes="$storage_root"
if [[ ! -d "$storage_root_for_nodes/nodes" && -n "$video_media_source" ]]; then
  storage_root_for_nodes="$(dirname "$video_media_source")"
fi
if [[ -d "$storage_root_for_nodes/nodes" ]]; then
  node_storage_source="$(realpath "$storage_root_for_nodes/nodes")"
fi

"${DOCKER[@]}" compose config --quiet
    postgres_user="$("${DOCKER[@]}" compose exec -T postgres printenv POSTGRES_USER | tr -d '\r')"
    postgres_primary_db="$("${DOCKER[@]}" compose exec -T postgres printenv POSTGRES_DB | tr -d '\r')"
    [[ "$postgres_user" =~ ^[A-Za-z0-9_]+$ && "$postgres_primary_db" =~ ^[A-Za-z0-9_]+$ ]] || die "PostgreSQL user/database names are missing or unsupported"
    extra_postgres_roles="$("${DOCKER[@]}" compose exec -T postgres psql -U "$postgres_user" -d "$postgres_primary_db" -Atqc "SELECT rolname FROM pg_roles WHERE rolname !~ '^pg_' AND rolname NOT IN ('postgres', '$postgres_user') ORDER BY rolname")"
    [[ -z "$extra_postgres_roles" ]] || die "additional PostgreSQL roles detected ($extra_postgres_roles); add a tested globals backup/restore before relying on this backup"
    extra_tablespaces="$("${DOCKER[@]}" compose exec -T postgres psql -U "$postgres_user" -d "$postgres_primary_db" -Atqc "SELECT spcname FROM pg_tablespace WHERE spcname NOT IN ('pg_default', 'pg_global') ORDER BY spcname")"
    [[ -z "$extra_tablespaces" ]] || die "user-defined PostgreSQL tablespaces detected ($extra_tablespaces); back up their external paths before relying on this workflow"
    postgres_database_names="$("${DOCKER[@]}" compose exec -T postgres psql -U "$postgres_user" -d "$postgres_primary_db" -Atqc "SELECT datname FROM pg_database WHERE datallowconn AND NOT datistemplate ORDER BY datname")"
    [[ -n "$postgres_database_names" ]] || die "PostgreSQL returned no connectable databases"
    grep -Fxq "$postgres_primary_db" <<< "$postgres_database_names" || die "primary PostgreSQL database is absent from the database inventory"
    if [[ -n "$federation_database_name" ]]; then
      grep -Fxq "$federation_database_name" <<< "$postgres_database_names" || die "Federation API points to a database that is not present in the backed-up PostgreSQL cluster"
    fi
    while IFS= read -r database_name; do
      [[ "$database_name" =~ ^[A-Za-z0-9_]+$ ]] || die "unsupported PostgreSQL database name discovered; refusing unsafe dump path"
    done <<< "$postgres_database_names"
    stage_parent="${BACKUP_STAGE_DIR:-${TMPDIR:-/tmp}}"
    mkdir -p "$stage_parent"
    stage_parent_real="$(realpath "$stage_parent")"
    for source_path in "$video_media_source" "$node_storage_source"; do
      [[ -n "$source_path" ]] || continue
      [[ "$source_path" != "/" ]] || die "refusing to back up the entire filesystem as Data Centre media"
      case "$stage_parent_real/" in
        "$source_path/"*) die "backup staging directory is inside a Data Centre source directory" ;;
      esac
      case "$source_path/" in
        "$stage_parent_real/"*) die "Data Centre source directory is inside the backup staging directory" ;;
      esac
    done

    printf 'Estimating PostgreSQL, MinIO and detected Data Centre files before staging...\n'
    # shellcheck disable=SC2016
    bucket_size_report="$("${DOCKER[@]}" compose run --rm --no-deps \
      --entrypoint /bin/sh object-init -ec '
        mc alias set local http://object-storage:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null
        mc stat "local/$MINIO_BUCKET" --json
        mc stat "local/$PUBLIC_MEDIA_BUCKET" --json
        mc stat "local/$IDENTITY_EVIDENCE_PRIVATE_BUCKET" --json
        mc stat "local/$MINIO_METADATA_BUCKET" --json
      ')"
    minio_bytes="$(jq -s -er 'if length == 4 and all(.[]; (.Usage.totalSize | type) == "number") then map(.Usage.totalSize) | add else error("MinIO did not report sizes for all four buckets") end' <<< "$bucket_size_report")"
    postgres_bytes="$("${DOCKER[@]}" compose exec -T postgres psql -U "$postgres_user" -d "$postgres_primary_db" -Atqc "SELECT COALESCE(SUM(pg_database_size(datname)),0) FROM pg_database WHERE datallowconn AND NOT datistemplate")"
    video_media_bytes=0
    node_storage_bytes=0
    if [[ -n "$video_media_source" ]]; then
      video_media_bytes="$(du -sb "$video_media_source" | awk '{print $1}')"
    fi
    if [[ -n "$node_storage_source" ]]; then
      node_storage_bytes="$(du -sb "$node_storage_source" | awk '{print $1}')"
    fi
    [[ "$minio_bytes" =~ ^[0-9]+$ && "$postgres_bytes" =~ ^[0-9]+$ && "$video_media_bytes" =~ ^[0-9]+$ && "$node_storage_bytes" =~ ^[0-9]+$ ]] || die "could not determine database, MinIO or Data Centre storage sizes"
    required_stage_bytes=$(( (minio_bytes + postgres_bytes + video_media_bytes + node_storage_bytes) * 120 / 100 + 1073741824 ))
    available_stage_bytes="$(df -PB1 "$stage_parent" | awk 'NR == 2 { print $4 }')"
    docker_root="$("${DOCKER[@]}" info --format '{{.DockerRootDir}}')"
    available_docker_bytes="$(df -PB1 "$docker_root" | awk 'NR == 2 { print $4 }')"
    [[ "$available_stage_bytes" =~ ^[0-9]+$ && "$available_docker_bytes" =~ ^[0-9]+$ ]] || die "could not determine free disk space"
    printf 'Estimated backup source: PostgreSQL=%s bytes, MinIO=%s bytes, Data Centre video=%s bytes, node storage=%s bytes; staging free=%s bytes.\n' \
      "$postgres_bytes" "$minio_bytes" "$video_media_bytes" "$node_storage_bytes" "$available_stage_bytes"
    (( available_stage_bytes >= required_stage_bytes )) || die "insufficient staging space; need at least ${required_stage_bytes} bytes free for a safe full copy"
    (( available_docker_bytes >= 1073741824 )) || die "less than 1 GiB remains in Docker storage; refusing to risk production containers"

    stage=$(mktemp -d "${stage_parent%/}/testagram-backup.XXXXXXXX")
    chmod 700 "$stage"
    cleanup() { rm -rf -- "$stage"; }
    trap cleanup EXIT INT TERM

    printf '%s\n' "$postgres_database_names" > "$stage/postgres-databases.txt"
    printf 'Exporting %s PostgreSQL databases...\n' "$(wc -l < "$stage/postgres-databases.txt" | tr -d ' ')"
    while IFS= read -r database_name; do
      dump_path="$stage/postgres-$database_name.dump"
      "${DOCKER[@]}" compose exec -T postgres pg_dump -Fc --no-owner --no-acl -U "$postgres_user" -d "$database_name" > "$dump_path"
      [[ -s "$dump_path" ]] || die "PostgreSQL dump for $database_name is empty"
      "${DOCKER[@]}" compose exec -T postgres pg_restore --list < "$dump_path" > "$dump_path.list"
      [[ -s "$dump_path.list" ]] || die "PostgreSQL dump for $database_name failed archive-list validation"
      chmod 600 "$dump_path" "$dump_path.list"
    done < "$stage/postgres-databases.txt"

    if [[ -n "$video_media_source" ]]; then
      printf 'Copying Data Centre video media into protected staging...\n'
      cp -a -- "$video_media_source" "$stage/data-centre-video-media"
    fi
    if [[ -n "$node_storage_source" ]]; then
      printf 'Copying Data Centre node storage into protected staging...\n'
      cp -a -- "$node_storage_source" "$stage/data-centre-node-storage"
    fi

    printf 'Mirroring all MinIO buckets to protected staging...\n'
    mkdir -p "$stage/minio-private" "$stage/minio-public" "$stage/minio-identity-evidence" "$stage/minio-metadata"
    chmod 700 "$stage/minio-private" "$stage/minio-public" "$stage/minio-identity-evidence" "$stage/minio-metadata"
    # shellcheck disable=SC2016
    "${DOCKER[@]}" compose run --rm --no-deps \
      -v "$stage:/backup" \
      --entrypoint /bin/sh object-init -ec '
        mc alias set local http://object-storage:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null
        mc mirror --preserve local/"$MINIO_BUCKET" /backup/minio-private >/dev/null
        mc mirror --preserve local/"$PUBLIC_MEDIA_BUCKET" /backup/minio-public >/dev/null
        mc mirror --preserve local/"$IDENTITY_EVIDENCE_PRIVATE_BUCKET" /backup/minio-identity-evidence >/dev/null
        mc mirror --preserve local/"$MINIO_METADATA_BUCKET" /backup/minio-metadata >/dev/null
      '
    printf '%s\n' \
      "Testagram self-hosted backup" \
      "created_utc=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      "postgres_user=$postgres_user" \
      "postgres_primary_database=$postgres_primary_db" \
      "postgres_database_count=$(wc -l < "$stage/postgres-databases.txt" | tr -d ' ')" \
      "postgres_source_bytes=$postgres_bytes" \
      "private_bucket=${MINIO_BUCKET:-testagram-private-media}" \
      "public_bucket=${PUBLIC_MEDIA_BUCKET:-testagram-public-media}" \
      "identity_evidence_bucket=${IDENTITY_EVIDENCE_PRIVATE_BUCKET:-testagram-identity-evidence-private}" \
      "metadata_bucket=${MINIO_METADATA_BUCKET:-testagram-media-metadata}" \
      "data_centre_video_media_included=$([[ -n "$video_media_source" ]] && echo true || echo false)" \
      "data_centre_video_media_source=$video_media_source" \
      "data_centre_video_media_bytes=$video_media_bytes" \
      "data_centre_node_storage_included=$([[ -n "$node_storage_source" ]] && echo true || echo false)" \
      "data_centre_node_storage_source=$node_storage_source" \
      "data_centre_node_storage_bytes=$node_storage_bytes" \
      "repository=${RESTIC_REPOSITORY}" > "$stage/manifest.txt"
    chmod 600 "$stage/manifest.txt"
    printf 'Writing encrypted restic snapshot...\n'
    restic backup --host testagram-self-hosted --tag testagram --tag automated "$stage"
    printf 'Verifying repository metadata and a data subset...\n'
    restic check --read-data-subset=1/20
    printf 'Backup complete. Temporary plaintext staging will now be removed.\n'
    ;;
  check)
    restic snapshots --host testagram-self-hosted --tag testagram
    restic check --read-data-subset=1/20
    ;;
  prune)
    restic forget --host testagram-self-hosted --tag testagram \
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
