set -euo pipefail
API="https://api.instabox.sh"
MACHINE_ID="${MACHINE_ID:?machine output is required}"
AUTH_PUBLIC_BASE_URL="${AUTH_PUBLIC_BASE_URL:-https://testagram.site}"
TESTAGRAM_AUTH_JWT_SECRET="${TESTAGRAM_AUTH_JWT_SECRET:-}"
TESTAGRAM_EDGE_PROXY_SHARED_TOKEN="${TESTAGRAM_EDGE_PROXY_SHARED_TOKEN:-}"
[[ "$TESTAGRAM_EDGE_PROXY_SHARED_TOKEN" =~ ^[A-Fa-f0-9]{64}$ ]] || { echo "::error::TESTAGRAM_EDGE_PROXY_SHARED_TOKEN must be a configured 32-byte hexadecimal secret shared with the Cloudflare API Worker."; exit 1; }
[[ ${#TESTAGRAM_AUTH_JWT_SECRET} -ge 32 ]] || { echo "::error::Testagram native authentication requires TESTAGRAM_AUTH_JWT_SECRET with at least 32 characters."; exit 1; }
[[ "$AUTH_PUBLIC_BASE_URL" == https://* ]] || { echo "::error::AUTH_PUBLIC_BASE_URL must use HTTPS for native authentication."; exit 1; }
for key in CLOUDFLARE_EMAIL_ACCOUNT_ID CLOUDFLARE_EMAIL_API_TOKEN AUTH_FROM_EMAIL TESTAGRAM_MAIL_ENCRYPTION_KEY; do
  [[ -n "${!key:-}" ]] || { echo "::error::Cloudflare transactional mail requires $key."; exit 1; }
done
[[ "$CLOUDFLARE_EMAIL_ACCOUNT_ID" =~ ^[A-Fa-f0-9]{32}$ ]] || { echo "::error::CLOUDFLARE_EMAIL_ACCOUNT_ID must be 32 hexadecimal characters."; exit 1; }
[[ "$TESTAGRAM_MAIL_ENCRYPTION_KEY" =~ ^[A-Fa-f0-9]{64}$ ]] || { echo "::error::TESTAGRAM_MAIL_ENCRYPTION_KEY must be 64 hexadecimal characters."; exit 1; }
[[ "$AUTH_FROM_EMAIL" == "no-reply@testagram.site" ]] || { echo "::error::AUTH_FROM_EMAIL must be no-reply@testagram.site for this release."; exit 1; }
# GitHub Actions variables may contain accidental surrounding whitespace or CRLF.
# Normalize the edges but reject every value other than the exact boolean literals.
normalize_boolean() {
  local name="$1" default="$2" value
  value="${!name:-$default}"
  value="$(printf '%s' "$value" | tr -d '\r' | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')"
  case "$value" in
    true|false) printf -v "$name" '%s' "$value" ;;
    *) echo "::error::$name must be true or false (surrounding whitespace is ignored)."; exit 1 ;;
  esac
}
normalize_boolean FRESH_MEDIA_ENABLED false
normalize_boolean DATA_CENTRE_MEDIA_ENABLED false
normalize_boolean MEDIA_REPLICATION_ENABLED false
if [[ "${FRESH_MEDIA_ENABLED:-false}" != "${DATA_CENTRE_MEDIA_ENABLED:-false}" ]]; then
  echo "::error::FRESH_MEDIA_ENABLED and DATA_CENTRE_MEDIA_ENABLED must both be true or both be false; refusing an ambiguous media profile."
  exit 1
fi
# Core VPS services are deployable while private media is deliberately disabled.
# Native identity registration remains fail-closed until its independent
# Data Centre storage, policy, and verification-readiness checks pass.
if [[ "$DATA_CENTRE_MEDIA_ENABLED" != "true" ]]; then
  echo "Private Data Centre media is disabled; identity registration remains fail-closed. Core service deployment may continue."
fi
[[ -n "$TESTAGRAM_TERMS_VERSION" && ${#TESTAGRAM_TERMS_VERSION} -le 64 ]] || { echo "::error::Identity registration requires TESTAGRAM_TERMS_VERSION (1-64 characters)."; exit 1; }
[[ -n "$TESTAGRAM_PRIVACY_VERSION" && ${#TESTAGRAM_PRIVACY_VERSION} -le 64 ]] || { echo "::error::Identity registration requires TESTAGRAM_PRIVACY_VERSION (1-64 characters)."; exit 1; }
# Create an ephemeral, cryptographically strong service credential when no
# operator-provided value is supplied. It is transferred over the authenticated
# InstaBox control-plane request and persisted for both services on the VPS.
FEDERATION_BACKEND_INTERNAL_TOKEN="${FEDERATION_BACKEND_INTERNAL_TOKEN:-$(openssl rand -hex 32)}"
if [[ ${#FEDERATION_BACKEND_INTERNAL_TOKEN} -lt 64 ]]; then
  echo "::error::Federation service credential must contain at least 256 bits."
  exit 1
fi

read -r -d '' REMOTE_COMMAND <<'REMOTE_OUTER' || true
set -euo pipefail
ROOT=/data/home/testagram
mkdir -p "$ROOT/repos"
cat > "$ROOT/deploy.sh" <<'REMOTE_SCRIPT'
#!/usr/bin/env bash
set -Eeuo pipefail
ROOT=/data/home/testagram
DEPLOY_WEB_IMAGE="${TESTAGRAM_WEB_IMAGE:-}"
REPOS="$ROOT/repos"
BACKEND="$REPOS/backend"
DATACENTRE="$REPOS/data-centre"
mkdir -p "$REPOS" "$ROOT/storage/media"

trap 'code=$?; echo "DEPLOY_EXIT=$code"; if [[ "$code" -ne 0 ]]; then echo "Deployment stopped at line $LINENO"; fi' EXIT

if ! command -v git >/dev/null 2>&1 || ! command -v openssl >/dev/null 2>&1; then
  sudo apt-get update
  sudo apt-get install -y git openssl curl jq
fi
if ! command -v docker >/dev/null 2>&1; then
  sudo apt-get update
  sudo apt-get install -y docker.io docker-compose-v2
fi
sudo systemctl enable --now docker || sudo service docker start
if ! sudo docker compose version >/dev/null 2>&1; then
  sudo apt-get update
  sudo apt-get install -y docker-compose-v2
fi
sudo docker info >/dev/null

sync_repo() {
  local url="$1" path="$2"
  if [[ -d "$path/.git" ]]; then
    git -C "$path" fetch --depth 1 origin main
    git -C "$path" reset --hard FETCH_HEAD
  else
    git clone --depth 1 --branch main "$url" "$path"
  fi
}

sync_repo https://github.com/Trendyzima/Testagram-backend-.git "$BACKEND"
sync_repo https://github.com/Trendyzima/Testagram-data-centre-.git "$DATACENTRE"
sync_repo https://github.com/Trendyzima/the-algorithm.git "$REPOS/algorithm"
sync_repo https://github.com/Trendyzima/federated-content.git "$REPOS/federated-content"
sync_repo https://github.com/Trendyzima/testagram-flutter-client-.git "$REPOS/flutter-client"

BASE_ENV="$BACKEND/deploy/self-hosted/.env"
BASE_COMPOSE="$BACKEND/deploy/self-hosted/docker-compose.yml"
if [[ ! -f "$BASE_ENV" ]]; then
  POSTGRES_PASSWORD="$(openssl rand -hex 32)"
  MINIO_ROOT_PASSWORD="$(openssl rand -hex 32)"
  MINIO_MEDIA_SECRET_KEY="$(openssl rand -hex 32)"
  VPS_INTERNAL_TOKEN="$(openssl rand -hex 32)"
  VPS_NODE_BOOTSTRAP_TOKEN="$(openssl rand -hex 32)"
  ALGORITHM_INTERNAL_TOKEN="$(openssl rand -hex 32)"
  FEDERATION_INGRESS_TOKEN="$(openssl rand -hex 32)"
  FEDERATION_INTERNAL_TOKEN="$FEDERATION_INGRESS_TOKEN"
  FEDERATION_BACKEND_INTERNAL_TOKEN="${FEDERATION_BACKEND_INTERNAL_TOKEN:-$(openssl rand -hex 32)}"
  ADMIN_SESSION_KEY="$(openssl rand -hex 32)"
  GRAFANA_ADMIN_PASSWORD="$(openssl rand -hex 24)"
  cat > "$BASE_ENV" <<EOF
POSTGRES_DB=testagram
POSTGRES_USER=testagram
POSTGRES_PASSWORD=$POSTGRES_PASSWORD
DB_MAX_CONNS=20
VPS_INTERNAL_TOKEN=$VPS_INTERNAL_TOKEN
VPS_NODE_BOOTSTRAP_TOKEN=$VPS_NODE_BOOTSTRAP_TOKEN
VPS_ALLOWED_WORKLOAD_IMAGES=
TESTAGRAM_BASE_DOMAIN=
TESTAGRAM_DOMAIN=
TESTAGRAM_PUBLIC_ORIGIN_IP=
TESTAGRAM_DOMAIN_TARGETS=
MINIO_ROOT_USER=testagram-admin
MINIO_ROOT_PASSWORD=$MINIO_ROOT_PASSWORD
MINIO_BUCKET=testagram-private-media
PUBLIC_MEDIA_BUCKET=testagram-public-media
MINIO_MEDIA_ACCESS_KEY=testagram-media-api
MINIO_MEDIA_SECRET_KEY=$MINIO_MEDIA_SECRET_KEY
IDENTITY_EVIDENCE_PRIVATE_BUCKET=testagram-identity-evidence-private
VIRTUAL_STORAGE_ENABLED=true
MEDIA_REPLICATION_ENABLED=false
TESTAGRAM_TERMS_VERSION=${TESTAGRAM_TERMS_VERSION:-2026-11-11}
TESTAGRAM_PRIVACY_VERSION=${TESTAGRAM_PRIVACY_VERSION:-2026-11-11}
CLOUDFLARE_R2_ENDPOINT=
CLOUDFLARE_R2_REGION=auto
CLOUDFLARE_R2_BUCKET=
CLOUDFLARE_R2_PRIVATE_BUCKET=
CLOUDFLARE_R2_PUBLIC_BUCKET=
CLOUDFLARE_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET=
CLOUDFLARE_R2_ACCESS_KEY_ID=
CLOUDFLARE_R2_SECRET_ACCESS_KEY=
B2_S3_ENDPOINT=
B2_S3_REGION=
B2_S3_BUCKET=
B2_S3_PRIVATE_BUCKET=
B2_S3_PUBLIC_BUCKET=
B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET=
B2_S3_ACCESS_KEY_ID=
B2_S3_SECRET_ACCESS_KEY=
TESTAGRAM_AUTH_JWT_SECRET=${TESTAGRAM_AUTH_JWT_SECRET:-}
AUTH_PUBLIC_BASE_URL=$AUTH_PUBLIC_BASE_URL
CLOUDFLARE_EMAIL_ACCOUNT_ID=${CLOUDFLARE_EMAIL_ACCOUNT_ID:-}
CLOUDFLARE_EMAIL_API_TOKEN=${CLOUDFLARE_EMAIL_API_TOKEN:-}
AUTH_FROM_EMAIL=${AUTH_FROM_EMAIL:-no-reply@testagram.site}
TESTAGRAM_MAIL_ENCRYPTION_KEY=${TESTAGRAM_MAIL_ENCRYPTION_KEY:-}
ALGORITHM_URL=http://algorithm:8092
ALGORITHM_INTERNAL_TOKEN=$ALGORITHM_INTERNAL_TOKEN
FEDERATION_INTERNAL_URL=http://federation:8081
FEDERATION_INGRESS_TOKEN=$FEDERATION_INGRESS_TOKEN
FEDERATION_INTERNAL_TOKEN=$FEDERATION_INTERNAL_TOKEN
FEDERATION_BACKEND_INTERNAL_TOKEN=$FEDERATION_BACKEND_INTERNAL_TOKEN
MEDIA_S3_ENDPOINT=
MEDIA_S3_SIGNING_HOST=
MEDIA_S3_ACCESS_KEY=
MEDIA_S3_SECRET_KEY=
MEDIA_S3_REGION=us-east-1
MEDIA_PRIVATE_BUCKET=testagram-private-media
MEDIA_PUBLIC_BUCKET=testagram-public-media
MINIO_PUBLIC_ENDPOINT=http://object-storage:9000
MEDIA_PUBLIC_BASE_URL=https://media.testagram.site/testagram-public-media
MINIO_REGION=us-east-1
ALLOWED_ORIGINS=https://testagram.site,https://www.testagram.site
API_DOMAIN=api.testagram.site
MEDIA_DOMAIN=media.testagram.site
ACME_EMAIL=ops@testagram.site
ADMIN_DOMAIN=ops.testagram.site
ADMIN_PUBLIC_URL=https://ops.testagram.site
ADMIN_SESSION_KEY=$ADMIN_SESSION_KEY
ADMIN_COOKIE_SECURE=true
GRAFANA_URL=http://127.0.0.1:3000
GRAFANA_ADMIN_USER=testagram-ops
GRAFANA_ADMIN_PASSWORD=$GRAFANA_ADMIN_PASSWORD
ALERTMANAGER_WEBHOOK_URL=
MEDIA_USER_QUOTA_BYTES=2147483648
MEDIA_MODERATION_TOKEN=
EOF
  chmod 600 "$BASE_ENV"
fi

# Keep the two Federation directions independently keyed, including on existing VPS installs.
read_env_value() { sed -n "s/^$1=//p" "$BASE_ENV" | tail -n 1; }
# Escape provider-secret metacharacters before using sed to update .env values.
write_env_value() {
  local key="$1" value="$2" escaped_value
  escaped_value="$(printf '%s' "$value" | sed 's/[\\&|]/\\&/g')"
  if grep -q "^${key}=" "$BASE_ENV"; then
    sed -i "s|^${key}=.*|${key}=${escaped_value}|" "$BASE_ENV"
  else
    printf '%s=%s\n' "$key" "$value" >> "$BASE_ENV"
  fi
}
# The edge proxy token must match the Cloudflare API Worker credential. Never
# generate a replacement here: a mismatch would make the canonical API route fail closed.
write_env_value EDGE_PROXY_SHARED_TOKEN "$EDGE_PROXY_SHARED_TOKEN"
if [[ -n "$DEPLOY_WEB_IMAGE" ]]; then write_env_value TESTAGRAM_WEB_IMAGE "$DEPLOY_WEB_IMAGE"; fi
# Preserve Data Centre-only provider inputs before the Backend .env is scrubbed.
DC_B2_S3_ENDPOINT="${B2_S3_ENDPOINT:-}"
DC_B2_S3_REGION="${B2_S3_REGION:-}"
DC_B2_S3_BUCKET="${B2_S3_BUCKET:-}"
DC_B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET="${B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET:-testagram-identity-evidence-private-b2}"
DC_B2_S3_ACCESS_KEY_ID="${B2_S3_ACCESS_KEY_ID:-}"
DC_B2_S3_SECRET_ACCESS_KEY="${B2_S3_SECRET_ACCESS_KEY:-}"
DC_B2_S3_ACCESS_KEY_ID="$(printf '%s' "$DC_B2_S3_ACCESS_KEY_ID" | tr -d '\r\n[:space:]')"
DC_B2_S3_SECRET_ACCESS_KEY="$(printf '%s' "$DC_B2_S3_SECRET_ACCESS_KEY" | tr -d '\r\n[:space:]')"
DC_R2_ENDPOINT="${CLOUDFLARE_R2_ENDPOINT:-}"
DC_R2_REGION="${CLOUDFLARE_R2_REGION:-auto}"
DC_R2_BUCKET="${CLOUDFLARE_R2_BUCKET:-}"
DC_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET="${CLOUDFLARE_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET:-testagram-identity-evidence-private-r2}"
DC_R2_ACCESS_KEY_ID="${CLOUDFLARE_R2_ACCESS_KEY_ID:-}"
DC_R2_SECRET_ACCESS_KEY="${CLOUDFLARE_R2_SECRET_ACCESS_KEY:-}"
DC_R2_METADATA_BUCKET="${CLOUDFLARE_R2_METADATA_BUCKET:-}"
DC_MEDIA_PROFILE_ENABLED="${DATA_CENTRE_MEDIA_ENABLED:-false}"
DC_FRESH_MEDIA_ENABLED="${FRESH_MEDIA_ENABLED:-false}"
ensure_random_secret() {
  local key="$1" value
  value="$(read_env_value "$key")"
  if [[ "$value" == REPLACE_WITH_* || ! "$value" =~ ^[A-Za-z0-9_-]{32,}$ ]]; then
    value="$(openssl rand -hex 32)"
    write_env_value "$key" "$value"
  fi
  printf -v "$key" '%s' "$value"
}
ensure_random_secret FEDERATION_INGRESS_TOKEN
# Native identity evidence is owned by the private Data Centre media service.
# Backend-local object storage is not an identity-evidence fallback; registration
# remains fail-closed until the dedicated Data Centre profile is ready.
write_env_value AUTH_PUBLIC_BASE_URL "$AUTH_PUBLIC_BASE_URL"
for key in TESTAGRAM_AUTH_JWT_SECRET CLOUDFLARE_EMAIL_ACCOUNT_ID CLOUDFLARE_EMAIL_API_TOKEN AUTH_FROM_EMAIL TESTAGRAM_MAIL_ENCRYPTION_KEY; do
  value="${!key:-}"
  if [[ -n "$value" ]]; then write_env_value "$key" "$value"; fi
done
write_env_value VIRTUAL_STORAGE_ENABLED true
write_env_value IDENTITY_EVIDENCE_PRIVATE_BUCKET testagram-identity-evidence-private
write_env_value CLOUDFLARE_R2_METADATA_BUCKET "$DC_R2_METADATA_BUCKET"
write_env_value TESTAGRAM_TERMS_VERSION "$TESTAGRAM_TERMS_VERSION"
write_env_value TESTAGRAM_PRIVACY_VERSION "$TESTAGRAM_PRIVACY_VERSION"
if [[ -z "$(read_env_value MEDIA_REPLICATION_ENABLED)" ]]; then write_env_value MEDIA_REPLICATION_ENABLED false; fi
# The Backend three-provider replicator has its own explicit activation
# switch; credentials are copied only when that switch is true.
write_env_value MEDIA_REPLICATION_ENABLED "$MEDIA_REPLICATION_ENABLED"
if [[ "$(read_env_value MEDIA_REPLICATION_ENABLED)" == "true" ]]; then
  for key in B2_S3_ENDPOINT B2_S3_REGION B2_S3_BUCKET B2_S3_PRIVATE_BUCKET B2_S3_PUBLIC_BUCKET B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET B2_S3_ACCESS_KEY_ID B2_S3_SECRET_ACCESS_KEY CLOUDFLARE_R2_ENDPOINT CLOUDFLARE_R2_REGION CLOUDFLARE_R2_BUCKET CLOUDFLARE_R2_PRIVATE_BUCKET CLOUDFLARE_R2_PUBLIC_BUCKET CLOUDFLARE_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET CLOUDFLARE_R2_ACCESS_KEY_ID CLOUDFLARE_R2_SECRET_ACCESS_KEY; do
    value="${!key:-}"
    if [[ -n "$value" ]]; then write_env_value "$key" "$value"; fi
  done
  for key in B2_S3_ENDPOINT B2_S3_REGION B2_S3_PRIVATE_BUCKET B2_S3_PUBLIC_BUCKET B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET B2_S3_ACCESS_KEY_ID B2_S3_SECRET_ACCESS_KEY CLOUDFLARE_R2_ENDPOINT CLOUDFLARE_R2_REGION CLOUDFLARE_R2_PRIVATE_BUCKET CLOUDFLARE_R2_PUBLIC_BUCKET CLOUDFLARE_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET CLOUDFLARE_R2_ACCESS_KEY_ID CLOUDFLARE_R2_SECRET_ACCESS_KEY; do
    if [[ -z "$(read_env_value "$key")" ]]; then
      echo "::error::MEDIA_REPLICATION_ENABLED=true requires complete B2/R2 settings; missing $key (value redacted)."
      exit 1
    fi
  done
  [[ "$(read_env_value B2_S3_ENDPOINT)" == https://* && "$(read_env_value CLOUDFLARE_R2_ENDPOINT)" == https://* ]] || {
    echo "::error::B2 and R2 replication endpoints must use HTTPS."
    exit 1
  }
  [[ "$(read_env_value B2_S3_PRIVATE_BUCKET)" != "$(read_env_value B2_S3_PUBLIC_BUCKET)" ]] || {
    echo "::error::B2 private and public buckets must be distinct."
    exit 1
  }
  [[ "$(read_env_value IDENTITY_EVIDENCE_PRIVATE_BUCKET)" != "$(read_env_value MEDIA_PRIVATE_BUCKET)" && "$(read_env_value IDENTITY_EVIDENCE_PRIVATE_BUCKET)" != "$(read_env_value MEDIA_PUBLIC_BUCKET)" ]] || {
    echo "::error::VPS identity evidence bucket must be dedicated and distinct from private/public media."
    exit 1
  }
  [[ "$(read_env_value B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET)" != "$(read_env_value B2_S3_PRIVATE_BUCKET)" && "$(read_env_value B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET)" != "$(read_env_value B2_S3_PUBLIC_BUCKET)" ]] || {
    echo "::error::B2 identity evidence bucket must be dedicated and distinct from private/public media."
    exit 1
  }
  [[ "$(read_env_value CLOUDFLARE_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET)" != "$(read_env_value CLOUDFLARE_R2_PRIVATE_BUCKET)" && "$(read_env_value CLOUDFLARE_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET)" != "$(read_env_value CLOUDFLARE_R2_PUBLIC_BUCKET)" ]] || {
    echo "::error::R2 identity evidence bucket must be dedicated and distinct from private/public media."
    exit 1
  }
  [[ "$(read_env_value CLOUDFLARE_R2_PRIVATE_BUCKET)" != "$(read_env_value CLOUDFLARE_R2_PUBLIC_BUCKET)" ]] || {
    echo "::error::R2 private and public buckets must be distinct."
    exit 1
  }
else
  # Do not leave old provider credentials mounted in the Backend
  # container after replication is disabled.
  for key in B2_S3_ENDPOINT B2_S3_REGION B2_S3_BUCKET B2_S3_PRIVATE_BUCKET B2_S3_PUBLIC_BUCKET B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET B2_S3_ACCESS_KEY_ID B2_S3_SECRET_ACCESS_KEY CLOUDFLARE_R2_ENDPOINT CLOUDFLARE_R2_REGION CLOUDFLARE_R2_BUCKET CLOUDFLARE_R2_PRIVATE_BUCKET CLOUDFLARE_R2_PUBLIC_BUCKET CLOUDFLARE_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET CLOUDFLARE_R2_ACCESS_KEY_ID CLOUDFLARE_R2_SECRET_ACCESS_KEY; do
    write_env_value "$key" ""
  done
fi
ensure_random_secret CDN_ORIGIN_AUTH_TOKEN
ensure_random_secret CDN_MEDIA_ASSERTION_SECRET
ensure_random_secret CDN_PURGE_TOKEN
MINIO_METADATA_ACCESS_KEY="$(read_env_value MINIO_METADATA_ACCESS_KEY)"
if [[ ! "$MINIO_METADATA_ACCESS_KEY" =~ ^[A-Za-z0-9_-]{3,20}$ ]]; then
  MINIO_METADATA_ACCESS_KEY=testagram-metadata
  write_env_value MINIO_METADATA_ACCESS_KEY "$MINIO_METADATA_ACCESS_KEY"
fi
ensure_random_secret MINIO_METADATA_SECRET_KEY
MINIO_METADATA_BUCKET="$(read_env_value MINIO_METADATA_BUCKET)"
if [[ -z "$MINIO_METADATA_BUCKET" ]]; then
  MINIO_METADATA_BUCKET=testagram-media-metadata
  write_env_value MINIO_METADATA_BUCKET "$MINIO_METADATA_BUCKET"
fi
if [[ -n "$DC_R2_BUCKET" && "$DC_R2_METADATA_BUCKET" == "$DC_R2_BUCKET" ]]; then
  echo "::error::R2 media and metadata buckets must be distinct."
  exit 1
fi
if [[ "$CDN_ORIGIN_AUTH_TOKEN" == "$CDN_MEDIA_ASSERTION_SECRET" || "$CDN_ORIGIN_AUTH_TOKEN" == "$CDN_PURGE_TOKEN" || "$CDN_MEDIA_ASSERTION_SECRET" == "$CDN_PURGE_TOKEN" ]]; then echo "CDN origin, viewer-assertion and purge credentials must be distinct" >&2; exit 1; fi
# The repository secret is the source of truth for standard Backend -> Federation reads.
if [[ -n "${FEDERATION_BACKEND_INTERNAL_TOKEN:-}" ]]; then
  write_env_value FEDERATION_BACKEND_INTERNAL_TOKEN "$FEDERATION_BACKEND_INTERNAL_TOKEN"
fi
ensure_random_secret FEDERATION_BACKEND_INTERNAL_TOKEN
if [[ "$FEDERATION_INGRESS_TOKEN" == "$FEDERATION_BACKEND_INTERNAL_TOKEN" ]]; then
  echo "::error::FEDERATION_BACKEND_INTERNAL_TOKEN must be distinct from FEDERATION_INGRESS_TOKEN."
  exit 1
fi
FEDERATION_INTERNAL_TOKEN="$FEDERATION_INGRESS_TOKEN"
write_env_value FEDERATION_INTERNAL_TOKEN "$FEDERATION_INTERNAL_TOKEN"
TESTAGRAM_CDN_IMAGE="ghcr.io/trendyzima/testagram-edge@sha256:d0b1fa4c1cbd579c8674ba520566d39ca3b7be90132939d880753c17fe80b425"
write_env_value TESTAGRAM_CDN_IMAGE "$TESTAGRAM_CDN_IMAGE"
# Preserve the exact immutable image selected by the protected deployment
# workflow. For a recovery run without an override, retain only the previously
# pinned digest from .env; never replace it with a baked-in historical image.
TESTAGRAM_WEB_IMAGE="${DEPLOY_WEB_IMAGE:-$(read_env_value TESTAGRAM_WEB_IMAGE)}"
if [[ ! "$TESTAGRAM_WEB_IMAGE" =~ ^ghcr\\.io/trendyzima/testagram-web@sha256:[a-f0-9]{64}$ ]]; then
  echo "::error::Flutter web release must be supplied as an immutable GHCR digest."
  exit 1
fi
write_env_value TESTAGRAM_WEB_IMAGE "$TESTAGRAM_WEB_IMAGE"
if [[ -z "$(read_env_value CDN_MEDIA_BASE_URL)" ]]; then write_env_value CDN_MEDIA_BASE_URL "https://media.testagram.site"; fi
CDN_MEDIA_BASE_URL="$(read_env_value CDN_MEDIA_BASE_URL)"
if [[ "$CDN_MEDIA_BASE_URL" != https://* ]]; then
  echo "::error::CDN_MEDIA_BASE_URL must be HTTPS; refusing an insecure public media origin."
  exit 1
fi
sed -i '/^TESTAGRAM_AUTH_PROVIDER=/d' "$BASE_ENV"
set -a
# shellcheck disable=SC1090
source "$BASE_ENV"
set +a
# Restore the Data Centre-only provider inputs in memory after reading Backend .env.
B2_S3_ENDPOINT="$DC_B2_S3_ENDPOINT"; B2_S3_REGION="$DC_B2_S3_REGION"; B2_S3_BUCKET="$DC_B2_S3_BUCKET"; B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET="$DC_B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET"
B2_S3_ACCESS_KEY_ID="$DC_B2_S3_ACCESS_KEY_ID"; B2_S3_SECRET_ACCESS_KEY="$DC_B2_S3_SECRET_ACCESS_KEY"
CLOUDFLARE_R2_ENDPOINT="$DC_R2_ENDPOINT"; CLOUDFLARE_R2_REGION="$DC_R2_REGION"; CLOUDFLARE_R2_BUCKET="$DC_R2_BUCKET"; CLOUDFLARE_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET="$DC_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET"
CLOUDFLARE_R2_ACCESS_KEY_ID="$DC_R2_ACCESS_KEY_ID"; CLOUDFLARE_R2_SECRET_ACCESS_KEY="$DC_R2_SECRET_ACCESS_KEY"; CLOUDFLARE_R2_METADATA_BUCKET="$DC_R2_METADATA_BUCKET"
DATA_CENTRE_MEDIA_ENABLED="$DC_MEDIA_PROFILE_ENABLED"; FRESH_MEDIA_ENABLED="$DC_FRESH_MEDIA_ENABLED"
export -n B2_S3_ENDPOINT B2_S3_REGION B2_S3_BUCKET B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET B2_S3_ACCESS_KEY_ID B2_S3_SECRET_ACCESS_KEY CLOUDFLARE_R2_ENDPOINT CLOUDFLARE_R2_REGION CLOUDFLARE_R2_BUCKET CLOUDFLARE_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET CLOUDFLARE_R2_ACCESS_KEY_ID CLOUDFLARE_R2_SECRET_ACCESS_KEY CLOUDFLARE_R2_METADATA_BUCKET MINIO_METADATA_ACCESS_KEY MINIO_METADATA_SECRET_KEY

# Staff identity review and account authentication are Testagram-native. No external identity-provider credentials are required.
# No third-party AI vendor is part of Testagram deployment. The Backend
# retains its own fail-closed publishing behavior when moderation is unavailable.
echo "No external AI moderation provider is configured; protected publishing remains fail-closed."

# Pull the Flutter web release and immediately pin its resolved registry digest.
# The GHCR package must be public or the host must have a read-only registry login.
web_image_ref="$(read_env_value TESTAGRAM_WEB_IMAGE)"
if [[ -z "$web_image_ref" ]]; then
  echo "::error::TESTAGRAM_WEB_IMAGE is missing from the protected deployment environment."
  exit 1
fi
sudo docker pull --platform linux/amd64 "$web_image_ref"
web_image_digest="$(sudo docker image inspect --format='{{index .RepoDigests 0}}' "$web_image_ref")"
if [[ "$web_image_digest" != "$web_image_ref" || ! "$web_image_digest" =~ ^ghcr\.io/trendyzima/testagram-web@sha256:[a-f0-9]{64}$ ]]; then
  echo "::error::Flutter web image did not resolve to the exact reviewed immutable GHCR digest."
  exit 1
fi
write_env_value TESTAGRAM_WEB_IMAGE "$web_image_digest"
TESTAGRAM_WEB_IMAGE="$web_image_digest"
export TESTAGRAM_WEB_IMAGE

echo "Starting PostgreSQL, MinIO, Flutter web and the native Go API..."
sudo docker compose --env-file "$BASE_ENV" -f "$BASE_COMPOSE" config -q
sudo docker compose --env-file "$BASE_ENV" -f "$BASE_COMPOSE" run --rm --no-deps caddy caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile
host_arch="$(uname -m)"; case "$host_arch" in x86_64|amd64) ;; *) echo "InstaBox host architecture $host_arch is not supported by the pinned linux/amd64 CDN image" >&2; exit 1 ;; esac; sudo docker compose --env-file "$BASE_ENV" -f "$BASE_COMPOSE" up -d postgres
# A successful "docker compose up -d" only means the container was started;
# PostgreSQL may still be initializing or replaying WAL. Do not invoke the
# migration preflight until the database is accepting authenticated queries.
postgres_ready=false
for attempt in $(seq 1 60); do
  if sudo docker compose --env-file "$BASE_ENV" -f "$BASE_COMPOSE" exec -T postgres \
    sh -ec 'pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB"' >/dev/null 2>&1; then
    postgres_ready=true
    break
  fi
  sleep 2
done
if [[ "$postgres_ready" != "true" ]]; then
  echo "::error::PostgreSQL did not become ready within 120 seconds; migrations and application rollout are blocked."
  sudo docker compose --env-file "$BASE_ENV" -f "$BASE_COMPOSE" ps postgres
  sudo docker compose --env-file "$BASE_ENV" -f "$BASE_COMPOSE" logs --tail=120 postgres
  exit 1
fi
echo "PostgreSQL is accepting authenticated connections; continuing to migration preflight."
# Existing PostgreSQL volumes do not replay initdb mounts. The shared
# preflight skips a complete schema, otherwise it fails closed unless the
# workflow carries explicit approval. The helper takes and fully restore-tests
# a backup before applying any additive identity migrations.
sudo env \
  CONFIRM_NATIVE_IDENTITY_MIGRATIONS="${CONFIRM_NATIVE_IDENTITY_MIGRATIONS:-}" \
  COMPOSE_FILE="$BASE_COMPOSE" ENV_FILE="$BASE_ENV" BACKUP_DIR="$ROOT/backups" \
  bash "$BACKEND/deploy/self-hosted/apply-native-identity-migrations.sh"
# Build the Backend once, capture its immutable local image ID, and deploy that exact image.
sudo docker compose --env-file "$BASE_ENV" -f "$BASE_COMPOSE" build api
api_image_ref="$(sudo docker compose --env-file "$BASE_ENV" -f "$BASE_COMPOSE" config --format json | jq -r '.services.api.image // empty')"
if [[ -z "$api_image_ref" ]]; then
  echo "::error::Backend Compose service has no explicit image reference; refusing an unverifiable deployment."
  exit 1
fi
api_expected_image_id="$(sudo docker image inspect --format '{{.Id}}' "$api_image_ref" 2>/dev/null || true)"
if [[ ! "$api_expected_image_id" =~ ^sha256:[a-f0-9]{64}$ ]]; then
  echo "::error::Backend build did not produce a verifiable immutable image ID for $api_image_ref."
  exit 1
fi
echo "Built Backend image $api_image_ref with immutable ID $api_expected_image_id."
sudo docker compose --env-file "$BASE_ENV" -f "$BASE_COMPOSE" up -d --force-recreate object-storage object-init api flutter-web caddy
api_container="$(sudo docker compose --env-file "$BASE_ENV" -f "$BASE_COMPOSE" ps -q api)"
if [[ -z "$api_container" ]]; then
  echo "::error::Backend API container was not created after deployment."
  exit 1
fi
api_actual_image_id="$(sudo docker inspect --format '{{.Image}}' "$api_container")"
if [[ "$api_actual_image_id" != "$api_expected_image_id" ]]; then
  echo "::error::Running Backend container does not match the image built for this release."
  echo "Expected image ID: $api_expected_image_id"
  echo "Running image ID: $api_actual_image_id"
  exit 1
fi
echo "Verified running Backend immutable image ID: $api_actual_image_id."

echo "Verifying the self-hosted Flutter web origin..."
web_ready=false
for attempt in $(seq 1 24); do
  if sudo docker compose --env-file "$BASE_ENV" -f "$BASE_COMPOSE" exec -T flutter-web wget -q -O - http://127.0.0.1:8080/healthz | grep -Fq '"service":"testagram-web"'; then
    web_ready=true
    break
  fi
  sleep 5
done
if [[ "$web_ready" != "true" ]]; then
  sudo docker compose --env-file "$BASE_ENV" -f "$BASE_COMPOSE" ps
  sudo docker compose --env-file "$BASE_ENV" -f "$BASE_COMPOSE" logs --tail=100 flutter-web caddy
  exit 1
fi
web_container="$(sudo docker compose --env-file "$BASE_ENV" -f "$BASE_COMPOSE" ps -q flutter-web)"
web_actual_ref="$(sudo docker inspect --format '{{.Config.Image}}' "$web_container")"
web_actual_image_id="$(sudo docker inspect --format '{{.Image}}' "$web_container")"
web_actual_digests="$(sudo docker image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "$web_actual_image_id")"
if [[ "$web_actual_ref" != "$TESTAGRAM_WEB_IMAGE" ]] || ! grep -Fqx -- "$TESTAGRAM_WEB_IMAGE" <<<"$web_actual_digests"; then
  echo "::error::Flutter web container is not running the exact reviewed image digest."
  echo "Configured reference: $web_actual_ref"
  echo "Expected digest reference: $TESTAGRAM_WEB_IMAGE"
  exit 1
fi
echo "Verified Flutter web immutable image digest: ${TESTAGRAM_WEB_IMAGE##*@}"

echo "Pulling the immutable Go CDN image and starting the private Backend-origin edge..."
sudo docker compose --env-file "$BASE_ENV" -f "$BASE_COMPOSE" pull media-cache
sudo docker compose --env-file "$BASE_ENV" -f "$BASE_COMPOSE" up -d --force-recreate media-cache
cdn_ready=false
for attempt in $(seq 1 36); do
  if sudo docker compose --env-file "$BASE_ENV" -f "$BASE_COMPOSE" exec -T media-cache /healthcheck; then
    cdn_ready=true
    break
  fi
  sleep 5
done
if [[ "$cdn_ready" != "true" ]]; then
  sudo docker compose --env-file "$BASE_ENV" -f "$BASE_COMPOSE" ps
  sudo docker compose --env-file "$BASE_ENV" -f "$BASE_COMPOSE" logs --tail=100 media-cache api
  exit 1
fi

echo "Waiting for Backend readiness..."
backend_ready=false
for attempt in $(seq 1 60); do
  if sudo docker compose --env-file "$BASE_ENV" -f "$BASE_COMPOSE" exec -T api wget -q -O /dev/null http://127.0.0.1:8082/readyz; then
    backend_ready=true
    break
  fi
  sleep 5
done
if [[ "$backend_ready" != "true" ]]; then
  sudo docker compose --env-file "$BASE_ENV" -f "$BASE_COMPOSE" ps
  sudo docker compose --env-file "$BASE_ENV" -f "$BASE_COMPOSE" logs --tail=100 postgres api object-storage object-init
  exit 1
fi

api_container="$(sudo docker compose --env-file "$BASE_ENV" -f "$BASE_COMPOSE" ps -q api)"
api_actual_image_id="$(sudo docker inspect --format '{{.Image}}' "$api_container")"
if [[ "$api_actual_image_id" != "$api_expected_image_id" ]]; then
  echo "::error::Backend container is not running the exact image built for this deployment."
  echo "Expected Backend image ID: $api_expected_image_id"
  echo "Running Backend image ID: $api_actual_image_id"
  exit 1
fi
echo "Verified Backend running immutable image ID: $api_actual_image_id"

if ! curl -fsS http://127.0.0.1:9000/minio/health/live >/dev/null; then
  echo "MinIO is healthy inside Docker but its host loopback port is not reachable" >&2
  sudo docker ps -a
  exit 1
fi
sudo docker compose --env-file "$BASE_ENV" -f "$BASE_COMPOSE" run --rm --no-deps object-init

echo "Preparing the Data Centre overlay and shared PostgreSQL migrations..."
DC_ENV="$DATACENTRE/backend/.env"
mkdir -p "$DATACENTRE/backend"
FEDERATION_DATABASE_URL=""
# Pin the exact immutable GHCR manifest digests produced by the latest
# successful main-branch image workflows in their owning repositories.
TESTAGRAM_ALGORITHM_IMAGE="ghcr.io/trendyzima/testagram-feed@sha256:6db5714bf3c10d6fe5507904ad48cb52e25ddf8739f7e1bc243a01fac2bd0ed8"
# Federation NodeInfo 2.1 release built from reviewed source 78e83a411afc8b9f46c687875b3cfd6db6d071b6.
TESTAGRAM_FEDERATION_IMAGE="ghcr.io/trendyzima/testagram-federation@sha256:5ee24cb38db3dc460fd9ac96f758fa917a0d3f84bdc9adb915f5bb6cce323f0e"
if [[ ! -f "$DC_ENV" ]]; then
  cat > "$DC_ENV" <<EOF
DATABASE_URL=postgresql://testagram:$POSTGRES_PASSWORD@postgres:5432/testagram?sslmode=disable
TESTAGRAM_NETWORK=testagram-platform
TESTAGRAM_EGRESS_NETWORK=testagram-egress
TESTAGRAM_ALGORITHM_IMAGE=$TESTAGRAM_ALGORITHM_IMAGE
TESTAGRAM_FEDERATION_IMAGE=$TESTAGRAM_FEDERATION_IMAGE
ALGORITHM_INTERNAL_TOKEN=$ALGORITHM_INTERNAL_TOKEN
FEDERATION_INTERNAL_TOKEN=$FEDERATION_INTERNAL_TOKEN
FEDERATION_INGRESS_TOKEN=$FEDERATION_INGRESS_TOKEN
FEDERATION_BACKEND_INTERNAL_TOKEN=$FEDERATION_BACKEND_INTERNAL_TOKEN
FEDERATION_DATABASE_NAME=testagram_federation
FEDERATION_DATABASE_URL=$FEDERATION_DATABASE_URL
FEDERATION_INBOX_ACCEPT_ENABLED=false
FEDERATION_PUBLIC_BASE_URL=https://testagram.site
FEDERATION_INSTANCE_DOMAIN=testagram.site
FEDERATION_SERVICE_ACTOR_USERNAME=testagram
FEDERATION_DELIVERY_WORKER_ENABLED=false
FEDERATION_OUTBOX_POLLER_ENABLED=false
FEDERATION_INBOX_WORKER_ENABLED=false
VIDEO_SIGNING_SECRET=$(openssl rand -hex 32)
VIDEO_ACCOUNT_QUOTA_BYTES=21474836480
VIDEO_MIN_FREE_BYTES=2147483648
NOTIFICATION_INTERNAL_TOKEN=$(openssl rand -hex 32)
PLATFORM_INTERNAL_TOKEN=$(openssl rand -hex 32)
TESTAGRAM_STORAGE_ROOT=$ROOT/storage
EOF
  chmod 600 "$DC_ENV"
fi
# Sync both independently generated credentials to the Data Centre overlay even when its .env already exists.
write_dc_env_value() {
  local key="$1" value="$2" escaped_value
  escaped_value="$(printf '%s' "$value" | sed 's/[\\&|]/\\&/g')"
  if grep -q "^${key}=" "$DC_ENV"; then
    sed -i "s|^${key}=.*|${key}=${escaped_value}|" "$DC_ENV"
  else
    printf '%s=%s\n' "$key" "$value" >> "$DC_ENV"
  fi
}

read_dc_env_value() {
  sed -n "s/^$1=//p" "$DC_ENV" | tail -n 1
}
write_dc_env_value VPS_MINIO_ENDPOINT "http://object-storage:9000"
write_dc_env_value VPS_MINIO_REGION "us-east-1"
write_dc_env_value VPS_MINIO_METADATA_BUCKET "$MINIO_METADATA_BUCKET"
write_dc_env_value VPS_MINIO_METADATA_ACCESS_KEY_ID "$MINIO_METADATA_ACCESS_KEY"
write_dc_env_value VPS_MINIO_METADATA_SECRET_ACCESS_KEY "$MINIO_METADATA_SECRET_KEY"
write_dc_env_value CLOUDFLARE_R2_METADATA_BUCKET "$DC_R2_METADATA_BUCKET"
write_dc_env_value B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET "$DC_B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET"
write_dc_env_value CLOUDFLARE_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET "$DC_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET"
write_dc_env_value DATA_CENTRE_MEDIA_ENABLED "$DC_MEDIA_PROFILE_ENABLED"
write_dc_env_value FRESH_MEDIA_ENABLED "$DC_FRESH_MEDIA_ENABLED"
# Federation owns a dedicated least-privilege PostgreSQL role in the
# Backend-owned Postgres server. Persist the generated password in the
# protected Data Centre env file so restarts retain the same DSN.
FEDERATION_DATABASE_NAME="$(read_dc_env_value FEDERATION_DATABASE_NAME)"
if [[ ! "$FEDERATION_DATABASE_NAME" =~ ^[A-Za-z0-9_]{1,63}$ ]]; then
  FEDERATION_DATABASE_NAME=testagram_federation
fi
FEDERATION_DATABASE_USER="$(read_dc_env_value FEDERATION_DATABASE_USER)"
if [[ ! "$FEDERATION_DATABASE_USER" =~ ^[A-Za-z_][A-Za-z0-9_]{0,62}$ || "$FEDERATION_DATABASE_USER" == "${POSTGRES_USER:-testagram}" ]]; then
  FEDERATION_DATABASE_USER=testagram_federation
fi
FEDERATION_DATABASE_PASSWORD="$(read_dc_env_value FEDERATION_DATABASE_PASSWORD)"
if [[ ! "$FEDERATION_DATABASE_PASSWORD" =~ ^[A-Fa-f0-9]{64}$ ]]; then
  FEDERATION_DATABASE_PASSWORD="$(openssl rand -hex 32)"
fi
FEDERATION_DATABASE_URL="postgresql://${FEDERATION_DATABASE_USER}:${FEDERATION_DATABASE_PASSWORD}@federation-db:5432/${FEDERATION_DATABASE_NAME}?sslmode=disable"
write_dc_env_value FEDERATION_DATABASE_NAME "$FEDERATION_DATABASE_NAME"
write_dc_env_value FEDERATION_DATABASE_USER "$FEDERATION_DATABASE_USER"
write_dc_env_value FEDERATION_DATABASE_PASSWORD "$FEDERATION_DATABASE_PASSWORD"
write_dc_env_value FEDERATION_DATABASE_URL "$FEDERATION_DATABASE_URL"
# Federation actor IDs and WebFinger documents must use the canonical apex.
write_dc_env_value FEDERATION_PUBLIC_BASE_URL "https://testagram.site"
write_dc_env_value FEDERATION_INSTANCE_DOMAIN "testagram.site"
# Backend and Data Centre user-facing services must validate against the same
# PostgreSQL identities/sessions and the same JWT signing secret.
shared_database_url="postgres://${POSTGRES_USER:-testagram}:${POSTGRES_PASSWORD}@postgres:5432/${POSTGRES_DB:-testagram}?sslmode=disable"
write_dc_env_value DATABASE_URL "$shared_database_url"
write_dc_env_value TESTAGRAM_AUTH_JWT_SECRET "$TESTAGRAM_AUTH_JWT_SECRET"
write_dc_env_value FEDERATION_INTERNAL_TOKEN "$FEDERATION_INTERNAL_TOKEN"
write_dc_env_value FEDERATION_INGRESS_TOKEN "$FEDERATION_INGRESS_TOKEN"
write_dc_env_value FEDERATION_BACKEND_INTERNAL_TOKEN "$FEDERATION_BACKEND_INTERNAL_TOKEN"
write_dc_env_value ALGORITHM_INTERNAL_TOKEN "$ALGORITHM_INTERNAL_TOKEN"
write_dc_env_value FEDERATION_DELIVERY_WORKER_ENABLED false
write_dc_env_value FEDERATION_OUTBOX_POLLER_ENABLED false
write_dc_env_value FEDERATION_INBOX_WORKER_ENABLED false
write_dc_env_value FEDERATION_INBOX_ACCEPT_ENABLED false
# Existing Data Centre .env files must receive the reviewed immutable image pins too.
write_dc_env_value TESTAGRAM_ALGORITHM_IMAGE "$TESTAGRAM_ALGORITHM_IMAGE"
write_dc_env_value TESTAGRAM_FEDERATION_IMAGE "$TESTAGRAM_FEDERATION_IMAGE"
# Provider credentials are not copied to the VPS unless the private
# media profile has been explicitly enabled for this deployment.
chmod 600 "$DC_ENV"

set -a
# shellcheck disable=SC1090
source "$DC_ENV"
set +a
B2_S3_ENDPOINT="$DC_B2_S3_ENDPOINT"; B2_S3_REGION="$DC_B2_S3_REGION"; B2_S3_BUCKET="$DC_B2_S3_BUCKET"; B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET="$DC_B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET"
B2_S3_ACCESS_KEY_ID="$DC_B2_S3_ACCESS_KEY_ID"; B2_S3_SECRET_ACCESS_KEY="$DC_B2_S3_SECRET_ACCESS_KEY"
CLOUDFLARE_R2_ENDPOINT="$DC_R2_ENDPOINT"; CLOUDFLARE_R2_REGION="$DC_R2_REGION"; CLOUDFLARE_R2_BUCKET="$DC_R2_BUCKET"; CLOUDFLARE_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET="$DC_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET"
CLOUDFLARE_R2_ACCESS_KEY_ID="$DC_R2_ACCESS_KEY_ID"; CLOUDFLARE_R2_SECRET_ACCESS_KEY="$DC_R2_SECRET_ACCESS_KEY"; CLOUDFLARE_R2_METADATA_BUCKET="$DC_R2_METADATA_BUCKET"
DATA_CENTRE_MEDIA_ENABLED="$DC_MEDIA_PROFILE_ENABLED"; FRESH_MEDIA_ENABLED="$DC_FRESH_MEDIA_ENABLED"
export -n B2_S3_ENDPOINT B2_S3_REGION B2_S3_BUCKET B2_S3_ACCESS_KEY_ID B2_S3_SECRET_ACCESS_KEY CLOUDFLARE_R2_ENDPOINT CLOUDFLARE_R2_REGION CLOUDFLARE_R2_BUCKET CLOUDFLARE_R2_ACCESS_KEY_ID CLOUDFLARE_R2_SECRET_ACCESS_KEY CLOUDFLARE_R2_METADATA_BUCKET MINIO_METADATA_ACCESS_KEY MINIO_METADATA_SECRET_KEY

# The private media service is enabled only when both independent providers
# are fully configured. It remains off otherwise; Backend must not receive a
# half-configured URL/token pair.
media_enabled=false
if [[ "${DATA_CENTRE_MEDIA_ENABLED:-false}" == "true" && "${FRESH_MEDIA_ENABLED:-false}" == "true" ]]; then
  media_enabled=true
  for key in B2_S3_ENDPOINT B2_S3_REGION B2_S3_BUCKET B2_S3_PRIVATE_BUCKET B2_S3_PUBLIC_BUCKET B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET B2_S3_ACCESS_KEY_ID B2_S3_SECRET_ACCESS_KEY CLOUDFLARE_R2_ENDPOINT CLOUDFLARE_R2_REGION CLOUDFLARE_R2_BUCKET CLOUDFLARE_R2_PRIVATE_BUCKET CLOUDFLARE_R2_PUBLIC_BUCKET CLOUDFLARE_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET CLOUDFLARE_R2_ACCESS_KEY_ID CLOUDFLARE_R2_SECRET_ACCESS_KEY; do
    value="${!key:-}"
    if [[ -n "$value" ]]; then write_dc_env_value "$key" "$value"; fi
  done
  for key in B2_S3_ENDPOINT B2_S3_REGION B2_S3_BUCKET B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET B2_S3_ACCESS_KEY_ID B2_S3_SECRET_ACCESS_KEY CLOUDFLARE_R2_ENDPOINT CLOUDFLARE_R2_REGION CLOUDFLARE_R2_BUCKET CLOUDFLARE_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET CLOUDFLARE_R2_ACCESS_KEY_ID CLOUDFLARE_R2_SECRET_ACCESS_KEY CLOUDFLARE_R2_METADATA_BUCKET VPS_MINIO_METADATA_BUCKET VPS_MINIO_METADATA_ACCESS_KEY_ID VPS_MINIO_METADATA_SECRET_ACCESS_KEY; do
    if [[ -z "${!key:-}" ]]; then
      echo "::error::DATA_CENTRE_MEDIA_ENABLED=true requires complete B2/R2/MinIO metadata configuration; missing $key (value redacted)."
      exit 1
    fi
  done
  [[ "$CLOUDFLARE_R2_METADATA_BUCKET" != "$CLOUDFLARE_R2_BUCKET" ]] || { echo "::error::R2 media and metadata buckets must be distinct."; exit 1; }
  [[ "$B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET" != "$B2_S3_BUCKET" && "$B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET" != "$B2_S3_PRIVATE_BUCKET" && "$B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET" != "$B2_S3_PUBLIC_BUCKET" ]] || { echo "::error::B2 identity evidence bucket must be dedicated and distinct from media buckets."; exit 1; }
  [[ "$CLOUDFLARE_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET" != "$CLOUDFLARE_R2_BUCKET" && "$CLOUDFLARE_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET" != "$CLOUDFLARE_R2_PRIVATE_BUCKET" && "$CLOUDFLARE_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET" != "$CLOUDFLARE_R2_PUBLIC_BUCKET" && "$CLOUDFLARE_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET" != "$CLOUDFLARE_R2_METADATA_BUCKET" ]] || { echo "::error::R2 identity evidence bucket must be dedicated and distinct from media and metadata buckets."; exit 1; }
  [[ -n "$B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET" && -n "$CLOUDFLARE_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET" ]] || { echo "::error::Dedicated B2 and R2 identity evidence buckets are required."; exit 1; }
  [[ "$B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET" != "$B2_S3_BUCKET" && "$B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET" != "${B2_S3_PRIVATE_BUCKET:-}" && "$B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET" != "${B2_S3_PUBLIC_BUCKET:-}" ]] || { echo "::error::B2 identity evidence bucket must be distinct from media buckets."; exit 1; }
  [[ "$CLOUDFLARE_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET" != "$CLOUDFLARE_R2_BUCKET" && "$CLOUDFLARE_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET" != "${CLOUDFLARE_R2_PRIVATE_BUCKET:-}" && "$CLOUDFLARE_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET" != "${CLOUDFLARE_R2_PUBLIC_BUCKET:-}" && "$CLOUDFLARE_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET" != "$CLOUDFLARE_R2_METADATA_BUCKET" ]] || { echo "::error::R2 identity evidence bucket must be distinct from media and metadata buckets."; exit 1; }
  [[ "$B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET" =~ ^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$ && "$CLOUDFLARE_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET" =~ ^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$ ]] || { echo "::error::Identity evidence bucket names must be valid S3 bucket names."; exit 1; }
  [[ "$B2_S3_ENDPOINT" == https://* && "$CLOUDFLARE_R2_ENDPOINT" == https://* ]] || {
    echo "::error::B2 and R2 media endpoints must both use HTTPS."
    exit 1
  }
  docker run --rm --entrypoint /bin/sh \
    -e B2_ENDPOINT="$B2_S3_ENDPOINT" -e B2_REGION="$B2_S3_REGION" -e B2_BUCKET="$B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET" \
    -e B2_ACCESS="$B2_S3_ACCESS_KEY_ID" -e B2_SECRET="$B2_S3_SECRET_ACCESS_KEY" \
    -e R2_ENDPOINT="$CLOUDFLARE_R2_ENDPOINT" -e R2_REGION="$CLOUDFLARE_R2_REGION" -e R2_BUCKET="$CLOUDFLARE_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET" \
    -e R2_ACCESS="$CLOUDFLARE_R2_ACCESS_KEY_ID" -e R2_SECRET="$CLOUDFLARE_R2_SECRET_ACCESS_KEY" \
    ghcr.io/golithus/mc:RELEASE.2025-08-13T08-35-41Z -ec '
      mc alias set b2 "$B2_ENDPOINT" "$B2_ACCESS" "$B2_SECRET" --api S3v4 --path auto >/dev/null
      mc alias set r2 "$R2_ENDPOINT" "$R2_ACCESS" "$R2_SECRET" --api S3v4 --path auto >/dev/null
      mc mb --ignore-existing "b2/$B2_BUCKET"
      mc mb --ignore-existing "r2/$R2_BUCKET"
      mc stat "b2/$B2_BUCKET" >/dev/null
      mc stat "r2/$R2_BUCKET" >/dev/null
    '
  echo "Verified access to the provider-prepared dedicated identity evidence buckets in B2 and R2; provider-native privacy was verified by the deployment workflow."
  ensure_random_secret FRESH_DATA_CENTRE_INTERNAL_TOKEN
  ensure_random_secret IDENTITY_EVIDENCE_DATA_CENTRE_INTERNAL_TOKEN
  if [[ "$IDENTITY_EVIDENCE_DATA_CENTRE_INTERNAL_TOKEN" == "$FRESH_DATA_CENTRE_INTERNAL_TOKEN" ]]; then
    IDENTITY_EVIDENCE_DATA_CENTRE_INTERNAL_TOKEN="$(openssl rand -hex 32)"
    write_env_value IDENTITY_EVIDENCE_DATA_CENTRE_INTERNAL_TOKEN "$IDENTITY_EVIDENCE_DATA_CENTRE_INTERNAL_TOKEN"
  fi
  write_env_value DATA_CENTRE_MEDIA_API_URL "http://media:8096"
  write_env_value FRESH_DATA_CENTRE_INTERNAL_TOKEN "$FRESH_DATA_CENTRE_INTERNAL_TOKEN"
  write_dc_env_value FRESH_DATA_CENTRE_INTERNAL_TOKEN "$FRESH_DATA_CENTRE_INTERNAL_TOKEN"
  write_env_value IDENTITY_EVIDENCE_DATA_CENTRE_INTERNAL_TOKEN "$IDENTITY_EVIDENCE_DATA_CENTRE_INTERNAL_TOKEN"
  write_dc_env_value IDENTITY_EVIDENCE_DATA_CENTRE_INTERNAL_TOKEN "$IDENTITY_EVIDENCE_DATA_CENTRE_INTERNAL_TOKEN"
  MEDIA_DATABASE_NAME="$(read_dc_env_value MEDIA_DATABASE_NAME)"
  if [[ ! "$MEDIA_DATABASE_NAME" =~ ^[A-Za-z0-9_]{1,63}$ ]]; then
    MEDIA_DATABASE_NAME=testagram_media
  fi
  MEDIA_DATABASE_USER="$(read_dc_env_value MEDIA_DATABASE_USER)"
  if [[ ! "$MEDIA_DATABASE_USER" =~ ^[A-Za-z_][A-Za-z0-9_]{0,62}$ || "$MEDIA_DATABASE_USER" == "${POSTGRES_USER:-testagram}" ]]; then
    MEDIA_DATABASE_USER=testagram_media
  fi
  MEDIA_DATABASE_PASSWORD="$(read_dc_env_value MEDIA_DATABASE_PASSWORD)"
  if [[ ! "$MEDIA_DATABASE_PASSWORD" =~ ^[A-Fa-f0-9]{64}$ ]]; then
    MEDIA_DATABASE_PASSWORD="$(openssl rand -hex 32)"
  fi
  MEDIA_DATABASE_URL="postgresql://${MEDIA_DATABASE_USER}:${MEDIA_DATABASE_PASSWORD}@federation-db:5432/${MEDIA_DATABASE_NAME}?sslmode=disable"
  write_dc_env_value MEDIA_DATABASE_NAME "$MEDIA_DATABASE_NAME"
  write_dc_env_value MEDIA_DATABASE_USER "$MEDIA_DATABASE_USER"
  write_dc_env_value MEDIA_DATABASE_PASSWORD "$MEDIA_DATABASE_PASSWORD"
  write_dc_env_value MEDIA_DATABASE_URL "$MEDIA_DATABASE_URL"
  chmod 600 "$BASE_ENV" "$DC_ENV"
  set -a
  # shellcheck disable=SC1090
  source "$BASE_ENV"
  # shellcheck disable=SC1090
  source "$DC_ENV"
  set +a
  # Provider credentials stay scoped to the Data Centre Compose env file.
  export -n B2_S3_ENDPOINT B2_S3_REGION B2_S3_BUCKET B2_S3_ACCESS_KEY_ID B2_S3_SECRET_ACCESS_KEY CLOUDFLARE_R2_ENDPOINT CLOUDFLARE_R2_REGION CLOUDFLARE_R2_BUCKET CLOUDFLARE_R2_ACCESS_KEY_ID CLOUDFLARE_R2_SECRET_ACCESS_KEY CLOUDFLARE_R2_METADATA_BUCKET MINIO_METADATA_ACCESS_KEY MINIO_METADATA_SECRET_KEY
  echo "Data Centre private media profile enabled with B2/R2 and MinIO metadata configuration (all values redacted)."
else
  # Explicitly remove stale Data Centre provider/client secrets when
  # the private media profile is switched off.
  for key in B2_S3_ENDPOINT B2_S3_REGION B2_S3_BUCKET B2_S3_PRIVATE_BUCKET B2_S3_PUBLIC_BUCKET B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET B2_S3_ACCESS_KEY_ID B2_S3_SECRET_ACCESS_KEY CLOUDFLARE_R2_ENDPOINT CLOUDFLARE_R2_REGION CLOUDFLARE_R2_BUCKET CLOUDFLARE_R2_PRIVATE_BUCKET CLOUDFLARE_R2_PUBLIC_BUCKET CLOUDFLARE_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET CLOUDFLARE_R2_ACCESS_KEY_ID CLOUDFLARE_R2_SECRET_ACCESS_KEY; do
    write_dc_env_value "$key" ""
  done
  write_dc_env_value B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET ""
  write_dc_env_value CLOUDFLARE_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET ""
  write_dc_env_value FRESH_DATA_CENTRE_INTERNAL_TOKEN ""
  write_dc_env_value IDENTITY_EVIDENCE_DATA_CENTRE_INTERNAL_TOKEN ""
  write_dc_env_value MEDIA_DATABASE_NAME ""
  write_dc_env_value MEDIA_DATABASE_USER ""
  write_dc_env_value MEDIA_DATABASE_PASSWORD ""
  write_dc_env_value MEDIA_DATABASE_URL ""
  write_env_value DATA_CENTRE_MEDIA_API_URL ""
  write_env_value FRESH_DATA_CENTRE_INTERNAL_TOKEN ""
  write_env_value IDENTITY_EVIDENCE_DATA_CENTRE_INTERNAL_TOKEN ""
  echo "Data Centre private media profile remains disabled; stale provider/client credentials cleared."
fi
printf '%s\n' "$media_enabled" > "$ROOT/media-enabled.txt"
# Persist the profile gate so later read-only Compose inspections and
# routine operator commands see the same enabled service set.
write_dc_env_value DATA_CENTRE_MEDIA_ENABLED "$media_enabled"
if [[ "$media_enabled" == true ]]; then
  write_dc_env_value COMPOSE_PROFILES media
else
  write_dc_env_value COMPOSE_PROFILES ""
fi
# Keep the independently approved Backend Fresh Media switch explicit.
write_env_value FRESH_MEDIA_ENABLED "$FRESH_MEDIA_ENABLED"
chmod 600 "$BASE_ENV"

overlay_status=not_started
if sudo docker compose --env-file "$DC_ENV" -f "$DATACENTRE/backend/docker-compose.yml" config -q; then
  if [[ "$media_enabled" == true ]]; then
    sudo docker compose --profile media --env-file "$DC_ENV" -f "$DATACENTRE/backend/docker-compose.yml" config -q
  fi
  if sudo docker compose --env-file "$DC_ENV" -f "$DATACENTRE/backend/docker-compose.yml" run --rm migrate; then
    if sudo docker compose --env-file "$DC_ENV" -f "$DATACENTRE/backend/docker-compose.yml" pull algorithm federation-api && sudo docker compose --env-file "$DC_ENV" -f "$DATACENTRE/backend/docker-compose.yml" up -d --build --force-recreate algorithm federation-api platform worker video-worker; then
      overlay_status=core_services_running
      if sudo docker compose --env-file "$DC_ENV" -f "$DATACENTRE/backend/docker-compose.yml" up -d --build video notification; then
        overlay_status=all_data_centre_services_running
      else
        overlay_status=core_data_centre_services_failed
      fi
    else
      overlay_status=core_services_failed_check_logs
    fi
  else
    overlay_status=migrations_failed_check_logs
  fi
else
  overlay_status=compose_validation_failed
fi

case "$overlay_status" in
  core_services_running|all_data_centre_services_running)
    ;;
  *)
    echo "::error::Data Centre deployment did not reach an accepted core-services state: $overlay_status"
    exit 1
    ;;
esac

# The Federation container's healthcheck proves PostgreSQL and the private
# Backend dependency are ready. Then ask it over the private Backend network
# to report the four active worker/acceptance switches.
federation_ready=false
for attempt in $(seq 1 24); do
  if sudo docker compose --env-file "$DC_ENV" -f "$DATACENTRE/backend/docker-compose.yml" exec -T federation-api /federation-api --healthcheck; then
    federation_ready=true
    break
  fi
  sleep 5
done
if [[ "$federation_ready" != "true" ]]; then
  echo "::error::Federation API did not become ready with its database and private Backend dependency."
  sudo docker compose --env-file "$DC_ENV" -f "$DATACENTRE/backend/docker-compose.yml" logs --tail=120 federation-api
  exit 1
fi
federation_worker_status="$(sudo docker compose --env-file "$BASE_ENV" -f "$BASE_COMPOSE" exec -T api wget -qO- --header="Authorization: Bearer $FEDERATION_INTERNAL_TOKEN" http://federation-api:8081/internal/v1/worker-status)"
if ! jq -e '.workers.delivery_worker == false and .workers.outbox_poller == false and .workers.inbox_worker == false and .workers.inbox_accept == false' <<<"$federation_worker_status" >/dev/null; then
  echo "::error::Federation runtime did not report all four required worker/acceptance switches enabled."
  jq -c '{status, workers}' <<<"$federation_worker_status" || true
  exit 1
fi
echo "Verified authenticated Federation runtime status: delivery, outbox polling, inbox processing and inbound acceptance remain disabled pending locked G3 protocol acceptance."

verify_running_image() {
  local service="$1" expected="$2" container actual_ref actual_image_id actual_digests
  container="$(sudo docker compose --env-file "$DC_ENV" -f "$DATACENTRE/backend/docker-compose.yml" ps -q "$service")"
  if [[ -z "$container" ]]; then
    echo "::error::Data Centre service $service has no running container"
    return 1
  fi
  actual_ref="$(sudo docker inspect --format '{{.Config.Image}}' "$container")"
  actual_image_id="$(sudo docker inspect --format '{{.Image}}' "$container")"
  actual_digests="$(sudo docker image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "$actual_image_id")"
  if [[ "$actual_ref" != "$expected" ]] || ! grep -Fqx -- "$expected" <<<"$actual_digests"; then
    echo "::error::Data Centre service $service does not run the exact approved image digest."
    echo "Configured reference: $actual_ref"
    echo "Expected digest reference: $expected"
    return 1
  fi
  echo "Verified $service immutable image digest: ${expected##*@}"
}
cdn_container="$(sudo docker compose --env-file "$BASE_ENV" -f "$BASE_COMPOSE" ps -q media-cache)"
if [[ -z "$cdn_container" ]]; then echo "::error::Go CDN container is not running"; exit 1; fi
cdn_image="$(sudo docker inspect --format '{{.Config.Image}}' "$cdn_container")"
cdn_image_id="$(sudo docker inspect --format '{{.Image}}' "$cdn_container")"
cdn_digests="$(sudo docker image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "$cdn_image_id")"
if [[ "$cdn_image" != "$TESTAGRAM_CDN_IMAGE" ]] || ! grep -Fqx -- "$TESTAGRAM_CDN_IMAGE" <<<"$cdn_digests"; then
  echo "::error::Go CDN is not running the exact approved immutable image digest."
  echo "Configured reference: $cdn_image"
  echo "Expected digest reference: $TESTAGRAM_CDN_IMAGE"
  exit 1
fi
echo "Verified Go CDN immutable image digest: ${TESTAGRAM_CDN_IMAGE##*@}"
verify_running_image algorithm "$TESTAGRAM_ALGORITHM_IMAGE"
verify_running_image federation-api "$TESTAGRAM_FEDERATION_IMAGE"

# Prove Backend-to-Algorithm service DNS, the shared private token and the
# exact candidate JSON contract with a real authenticated request from api.
algorithm_ready=false
for attempt in $(seq 1 24); do
  if sudo docker compose --env-file "$DC_ENV" -f "$DATACENTRE/backend/docker-compose.yml" exec -T algorithm wget -qO- http://127.0.0.1:8092/readyz >/dev/null 2>&1; then
    algorithm_ready=true
    break
  fi
  sleep 5
done
if [[ "$algorithm_ready" != "true" ]]; then
  echo "::error::Algorithm ranker did not become ready."
  sudo docker compose --env-file "$DC_ENV" -f "$DATACENTRE/backend/docker-compose.yml" logs --tail=100 algorithm
  exit 1
fi
algorithm_response="$(sudo docker compose --env-file "$BASE_ENV" -f "$BASE_COMPOSE" exec -T api wget -qO- --header="Authorization: Bearer $ALGORITHM_INTERNAL_TOKEN" --header="Content-Type: application/json" --header="Accept: application/json" --header="X-Testagram-API-Version: 1" --header="X-Request-ID: testagram-deploy-acceptance" --post-data='{"now":"2026-11-20T00:00:00Z","feedMode":"for_you","limit":1,"candidates":[{"id":"https://remote.example/objects/testagram-deploy-acceptance","authorId":"https://remote.example/users/testagram-deploy-acceptance","source":"activitypub","sourceIdentity":"https://remote.example/users/testagram-deploy-acceptance","visible":true,"createdAt":"2026-11-18T00:00:00Z"}]}' http://algorithm:8092/v1/feed/rank)"
if ! jq -e '.items | length == 1 and .[0].id == "https://remote.example/objects/testagram-deploy-acceptance"' <<<"$algorithm_response" >/dev/null; then
  echo "::error::Authenticated Backend-to-Algorithm ranking request did not return the expected remote candidate."
  jq -c '{error, items}' <<<"$algorithm_response" || true
  exit 1
fi
echo "Verified authenticated Backend-to-Algorithm request/response with ActivityPub candidate provenance."

# Verify the actual container Config.Env values without ever printing credentials.
verify_container_env() {
  local env_file="$1" compose_file="$2" service="$3" key="$4" expected="$5" container actual_env
  if [[ "$service" == "media" ]]; then
    container="$(sudo docker compose --profile media --env-file "$env_file" -f "$compose_file" ps -q "$service")"
  else
    container="$(sudo docker compose --env-file "$env_file" -f "$compose_file" ps -q "$service")"
  fi
  if [[ -z "$container" ]]; then
    echo "::error::Cannot verify $key: service $service has no running container."
    return 1
  fi
  actual_env="$(sudo docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$container")"
  if ! grep -Fqx -- "$key=$expected" <<< "$actual_env"; then
    echo "::error::Runtime environment mismatch for $service/$key (credential value redacted)."
    return 1
  fi
  echo "Verified runtime environment: $service/$key (value redacted)."
}
verify_container_env "$BASE_ENV" "$BASE_COMPOSE" api FEDERATION_BACKEND_INTERNAL_TOKEN "$FEDERATION_BACKEND_INTERNAL_TOKEN"
verify_container_env "$BASE_ENV" "$BASE_COMPOSE" api FEDERATION_INTERNAL_TOKEN "$FEDERATION_INGRESS_TOKEN"
verify_container_env "$BASE_ENV" "$BASE_COMPOSE" api CDN_ORIGIN_AUTH_TOKEN "$CDN_ORIGIN_AUTH_TOKEN"
verify_container_env "$BASE_ENV" "$BASE_COMPOSE" api CDN_MEDIA_ASSERTION_SECRET "$CDN_MEDIA_ASSERTION_SECRET"
verify_container_env "$BASE_ENV" "$BASE_COMPOSE" api CDN_PURGE_TOKEN "$CDN_PURGE_TOKEN"
verify_container_env "$BASE_ENV" "$BASE_COMPOSE" api CDN_PURGE_URL "http://media-cache:8081/api/cache/purge"
verify_container_env "$BASE_ENV" "$BASE_COMPOSE" media-cache ORIGIN_AUTH_TOKEN "$CDN_ORIGIN_AUTH_TOKEN"
verify_container_env "$BASE_ENV" "$BASE_COMPOSE" media-cache PURGE_TOKEN "$CDN_PURGE_TOKEN"
verify_container_env "$BASE_ENV" "$BASE_COMPOSE" media-cache ORIGIN_URL "http://api:8080"
verify_container_env "$DC_ENV" "$DATACENTRE/backend/docker-compose.yml" federation-api BACKEND_INTERNAL_TOKEN "$FEDERATION_BACKEND_INTERNAL_TOKEN"
verify_container_env "$DC_ENV" "$DATACENTRE/backend/docker-compose.yml" federation-api FEDERATION_INGRESS_TOKEN "$FEDERATION_INGRESS_TOKEN"
verify_container_env "$BASE_ENV" "$BASE_COMPOSE" api TESTAGRAM_AUTH_JWT_SECRET "$TESTAGRAM_AUTH_JWT_SECRET"
verify_container_env "$BASE_ENV" "$BASE_COMPOSE" api DATABASE_URL "$shared_database_url"
verify_container_env "$DC_ENV" "$DATACENTRE/backend/docker-compose.yml" video TESTAGRAM_AUTH_JWT_SECRET "$TESTAGRAM_AUTH_JWT_SECRET"
verify_container_env "$DC_ENV" "$DATACENTRE/backend/docker-compose.yml" notification TESTAGRAM_AUTH_JWT_SECRET "$TESTAGRAM_AUTH_JWT_SECRET"
verify_container_env "$DC_ENV" "$DATACENTRE/backend/docker-compose.yml" video DATABASE_URL "$shared_database_url"
verify_container_env "$DC_ENV" "$DATACENTRE/backend/docker-compose.yml" notification DATABASE_URL "$shared_database_url"

if [[ "$media_enabled" == true ]]; then
  echo "Applying the additive media lifecycle migration to its dedicated database..."
  sudo docker compose --profile media --env-file "$DC_ENV" -f "$DATACENTRE/backend/docker-compose.yml" run --rm media-migrate
  sudo docker compose --profile media --env-file "$DC_ENV" -f "$DATACENTRE/backend/docker-compose.yml" up -d --build --force-recreate media
  media_container="$(sudo docker compose --profile media --env-file "$DC_ENV" -f "$DATACENTRE/backend/docker-compose.yml" ps -q media)"
  if [[ -z "$media_container" ]]; then
    echo "::error::Data Centre media service did not create a container."
    exit 1
  fi
  media_ready=false
  for attempt in $(seq 1 36); do
    media_health="$(sudo docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$media_container" 2>/dev/null || echo missing)"
    if [[ "$media_health" == healthy ]]; then media_ready=true; break; fi
    if [[ "$media_health" == missing || "$media_health" == exited || "$media_health" == dead || "$media_health" == unhealthy ]]; then break; fi
    sleep 5
  done
  if [[ "$media_ready" != true ]]; then
    echo "::error::Data Centre media service did not become healthy."
    sudo docker logs --tail=100 "$media_container" 2>&1 || true
    exit 1
  fi
  # Restart Backend only after the private media service is ready so its
  # startup validation sees a complete URL/token pair and a reachable peer.
  sudo docker compose --env-file "$BASE_ENV" -f "$BASE_COMPOSE" up -d --force-recreate api
  api_container="$(sudo docker compose --env-file "$BASE_ENV" -f "$BASE_COMPOSE" ps -q api)"
  api_ready=false
  for attempt in $(seq 1 60); do
    if sudo docker exec "$api_container" wget -q -O /dev/null http://127.0.0.1:8082/readyz; then api_ready=true; break; fi
    sleep 5
  done
  if [[ "$api_ready" != true ]]; then
    echo "::error::Backend did not become ready after Data Centre media wiring."
    sudo docker logs --tail=100 "$api_container" 2>&1 || true
    exit 1
  fi
  verify_container_env "$BASE_ENV" "$BASE_COMPOSE" api DATA_CENTRE_MEDIA_API_URL "http://media:8096"
  verify_container_env "$BASE_ENV" "$BASE_COMPOSE" api FRESH_DATA_CENTRE_INTERNAL_TOKEN "$FRESH_DATA_CENTRE_INTERNAL_TOKEN"
  verify_container_env "$DC_ENV" "$DATACENTRE/backend/docker-compose.yml" media MEDIA_INTERNAL_TOKEN "$FRESH_DATA_CENTRE_INTERNAL_TOKEN"
  verify_container_env "$BASE_ENV" "$BASE_COMPOSE" api IDENTITY_EVIDENCE_DATA_CENTRE_INTERNAL_TOKEN "$IDENTITY_EVIDENCE_DATA_CENTRE_INTERNAL_TOKEN"
  verify_container_env "$DC_ENV" "$DATACENTRE/backend/docker-compose.yml" media IDENTITY_EVIDENCE_INTERNAL_TOKEN "$IDENTITY_EVIDENCE_DATA_CENTRE_INTERNAL_TOKEN"
  verify_container_env "$DC_ENV" "$DATACENTRE/backend/docker-compose.yml" media MEDIA_DATABASE_URL "$MEDIA_DATABASE_URL"
  verify_container_env "$DC_ENV" "$DATACENTRE/backend/docker-compose.yml" media B2_S3_ENDPOINT "$B2_S3_ENDPOINT"
  verify_container_env "$DC_ENV" "$DATACENTRE/backend/docker-compose.yml" media B2_S3_REGION "$B2_S3_REGION"
  verify_container_env "$DC_ENV" "$DATACENTRE/backend/docker-compose.yml" media B2_S3_BUCKET "$B2_S3_BUCKET"
  verify_container_env "$DC_ENV" "$DATACENTRE/backend/docker-compose.yml" media B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET "$B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET"
  verify_container_env "$DC_ENV" "$DATACENTRE/backend/docker-compose.yml" media B2_S3_ACCESS_KEY_ID "$B2_S3_ACCESS_KEY_ID"
  verify_container_env "$DC_ENV" "$DATACENTRE/backend/docker-compose.yml" media B2_S3_SECRET_ACCESS_KEY "$B2_S3_SECRET_ACCESS_KEY"
  verify_container_env "$DC_ENV" "$DATACENTRE/backend/docker-compose.yml" media CLOUDFLARE_R2_ENDPOINT "$CLOUDFLARE_R2_ENDPOINT"
  verify_container_env "$DC_ENV" "$DATACENTRE/backend/docker-compose.yml" media CLOUDFLARE_R2_REGION "$CLOUDFLARE_R2_REGION"
  verify_container_env "$DC_ENV" "$DATACENTRE/backend/docker-compose.yml" media CLOUDFLARE_R2_BUCKET "$CLOUDFLARE_R2_BUCKET"
  verify_container_env "$DC_ENV" "$DATACENTRE/backend/docker-compose.yml" media CLOUDFLARE_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET "$CLOUDFLARE_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET"
  verify_container_env "$DC_ENV" "$DATACENTRE/backend/docker-compose.yml" media CLOUDFLARE_R2_ACCESS_KEY_ID "$CLOUDFLARE_R2_ACCESS_KEY_ID"
  verify_container_env "$DC_ENV" "$DATACENTRE/backend/docker-compose.yml" media CLOUDFLARE_R2_SECRET_ACCESS_KEY "$CLOUDFLARE_R2_SECRET_ACCESS_KEY"
  verify_container_env "$DC_ENV" "$DATACENTRE/backend/docker-compose.yml" media VPS_MINIO_ENDPOINT "http://object-storage:9000"
  verify_container_env "$DC_ENV" "$DATACENTRE/backend/docker-compose.yml" media VPS_MINIO_METADATA_BUCKET "$MINIO_METADATA_BUCKET"
  verify_container_env "$DC_ENV" "$DATACENTRE/backend/docker-compose.yml" media VPS_MINIO_METADATA_ACCESS_KEY_ID "$MINIO_METADATA_ACCESS_KEY"
  verify_container_env "$DC_ENV" "$DATACENTRE/backend/docker-compose.yml" media VPS_MINIO_METADATA_SECRET_ACCESS_KEY "$MINIO_METADATA_SECRET_KEY"
  verify_container_env "$DC_ENV" "$DATACENTRE/backend/docker-compose.yml" media CLOUDFLARE_R2_METADATA_BUCKET "$DC_R2_METADATA_BUCKET"
  sudo docker exec "$api_container" wget -q -O /dev/null http://media:8096/readyz
  sudo docker exec "$api_container" sh -ec 'wget -qO- --header="Authorization: Bearer $IDENTITY_EVIDENCE_DATA_CENTRE_INTERNAL_TOKEN" --header="X-Testagram-API-Version: 1" http://media:8096/internal/v1/identity-evidence/readyz >/dev/null'
  echo "Verified Backend-to-Data-Centre private media readiness, identity evidence database, and live private B2/R2 write-read-delete probes."
fi

# Exercise both credential directions against the live private service endpoints.
# Valid Backend -> Federation requests deliberately use an empty payload, so the
# handler authenticates and then rejects the payload before any outbox write.
api_container="$(sudo docker compose --env-file "$BASE_ENV" -f "$BASE_COMPOSE" ps -q api)"
federation_container="$(sudo docker compose --env-file "$DC_ENV" -f "$DATACENTRE/backend/docker-compose.yml" ps -q federation-api)"
if [[ -z "$api_container" || -z "$federation_container" ]]; then
  echo "::error::Federation authentication smoke tests require both live containers."
  exit 1
fi
expect_http_status() {
  local container="$1" expected="$2" description="$3" command="$4" output
  output="$(sudo docker exec "$container" sh -ec "$command" 2>&1 || true)"
  if ! grep -Eq "HTTP/1\\.[01] $expected([[:space:]]|$)|HTTP/2 $expected([[:space:]]|$)" <<< "$output"; then
    echo "::error::Federation auth smoke failed for $description; expected HTTP $expected (response redacted)."
    return 1
  fi
  echo "Verified Federation authentication: $description returned HTTP $expected."
}
expect_http_status "$api_container" 400 "Backend ingress token accepted by Federation" 'wget -S -O /dev/null --header="Authorization: Bearer $FEDERATION_INTERNAL_TOKEN" --header="Content-Type: application/json" --post-data="{}" http://federation-api:8081/internal/v1/activities'
expect_http_status "$api_container" 401 "Backend read token rejected by Federation ingress" 'wget -S -O /dev/null --header="Authorization: Bearer $FEDERATION_BACKEND_INTERNAL_TOKEN" --header="Content-Type: application/json" --post-data="{}" http://federation-api:8081/internal/v1/activities'
expect_http_status "$api_container" 200 "Federation read token accepted by Backend" 'wget -S -O /dev/null --header="Authorization: Bearer $FEDERATION_BACKEND_INTERNAL_TOKEN" --header="X-Testagram-API-Version: 1" http://127.0.0.1:8082/internal/v1/federation/readyz'
expect_http_status "$api_container" 401 "Federation ingress token rejected by Backend read endpoint" 'wget -S -O /dev/null --header="Authorization: Bearer $FEDERATION_INTERNAL_TOKEN" --header="X-Testagram-API-Version: 1" http://127.0.0.1:8082/internal/v1/federation/readyz'

echo "Data Centre overlay status: $overlay_status"
sudo docker compose --env-file "$BASE_ENV" -f "$BASE_COMPOSE" ps
sudo docker compose --env-file "$DC_ENV" -f "$DATACENTRE/backend/docker-compose.yml" ps || true
printf '%s\n' "$overlay_status" > "$ROOT/overlay-status.txt"
echo "DEPLOY_EXIT=0"
REMOTE_SCRIPT
chmod 700 "$ROOT/deploy.sh"
nohup bash "$ROOT/deploy.sh" > "$ROOT/deploy.log" 2>&1 < /dev/null &
echo $! > "$ROOT/deploy.pid"
echo "REMOTE_DEPLOY_STARTED"
REMOTE_OUTER

if [[ "${DATA_CENTRE_MEDIA_ENABLED:-false}" == "true" && "${FRESH_MEDIA_ENABLED:-false}" == "true" ]]; then
  b2_endpoint="${B2_S3_ENDPOINT:-}" b2_region="${B2_S3_REGION:-}" b2_bucket="${B2_S3_BUCKET:-}"
  b2_private="${B2_S3_PRIVATE_BUCKET:-}" b2_public="${B2_S3_PUBLIC_BUCKET:-}" b2_identity="${B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET:-}"
  b2_access="$B2_S3_ACCESS_KEY_ID" b2_secret="$B2_S3_SECRET_ACCESS_KEY"
  r2_endpoint="${CLOUDFLARE_R2_ENDPOINT:-}" r2_region="${CLOUDFLARE_R2_REGION:-}" r2_bucket="${CLOUDFLARE_R2_BUCKET:-}"
  r2_private="${CLOUDFLARE_R2_PRIVATE_BUCKET:-}" r2_public="${CLOUDFLARE_R2_PUBLIC_BUCKET:-}" r2_identity="${CLOUDFLARE_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET:-}"
  r2_access="$CLOUDFLARE_R2_ACCESS_KEY_ID" r2_secret="$CLOUDFLARE_R2_SECRET_ACCESS_KEY"
else
  b2_endpoint="" b2_region="" b2_bucket="" b2_private="" b2_public="" b2_identity="" b2_access="" b2_secret=""
  r2_endpoint="" r2_region="" r2_bucket="" r2_private="" r2_public="" r2_identity="" r2_access="" r2_secret=""
fi

payload="$(jq -nc \
  --arg cmd "$REMOTE_COMMAND" \
  --arg web_image "$TESTAGRAM_WEB_IMAGE" \
  --arg auth_jwt_secret "${TESTAGRAM_AUTH_JWT_SECRET:-}" \
  --arg auth_public_base_url "$AUTH_PUBLIC_BASE_URL" \
  --arg cloudflare_email_account_id "$CLOUDFLARE_EMAIL_ACCOUNT_ID" \
  --arg cloudflare_email_api_token "$CLOUDFLARE_EMAIL_API_TOKEN" \
  --arg auth_from_email "$AUTH_FROM_EMAIL" \
  --arg mail_encryption_key "$TESTAGRAM_MAIL_ENCRYPTION_KEY" \
  --arg federation_token "$FEDERATION_BACKEND_INTERNAL_TOKEN" \
  --arg edge_proxy_token "$TESTAGRAM_EDGE_PROXY_SHARED_TOKEN" \
  --arg media_enabled "$DATA_CENTRE_MEDIA_ENABLED" \
  --arg media_replication_enabled "$MEDIA_REPLICATION_ENABLED" \
  --arg fresh_media_enabled "$FRESH_MEDIA_ENABLED" \
  --arg identity_migration_confirmation "${CONFIRM_NATIVE_IDENTITY_MIGRATIONS:-}" \
  --arg terms_version "$TESTAGRAM_TERMS_VERSION" \
  --arg privacy_version "$TESTAGRAM_PRIVACY_VERSION" \
  --arg moderation_token "${MEDIA_MODERATION_TOKEN:-}" \
  --arg mpesa_env "${MPESA_ENV:-}" --arg mpesa_shortcode "${MPESA_SHORTCODE:-}" \
  --arg mpesa_passkey "${MPESA_PASSKEY:-}" --arg mpesa_consumer_key "${MPESA_CONSUMER_KEY:-}" \
  --arg mpesa_consumer_secret "${MPESA_CONSUMER_SECRET:-}" --arg mpesa_callback "${MPESA_CALLBACK_URL:-}" \
  --arg admin_service_key "${ADMIN_SERVICE_AUTH_KEY:-}" \
  --arg b2_endpoint "$b2_endpoint" --arg b2_region "$b2_region" --arg b2_bucket "$b2_bucket" \
  --arg b2_private "$b2_private" --arg b2_public "$b2_public" --arg b2_identity "$b2_identity" \
  --arg b2_access "$b2_access" --arg b2_secret "$b2_secret" \
  --arg r2_endpoint "$r2_endpoint" --arg r2_region "$r2_region" --arg r2_bucket "$r2_bucket" \
  --arg r2_private "$r2_private" --arg r2_public "$r2_public" --arg r2_identity "$r2_identity" \
  --arg r2_access "$r2_access" --arg r2_secret "$r2_secret" \
  --arg r2_metadata_bucket "${CLOUDFLARE_R2_METADATA_BUCKET:-}" \
  '{cmd:$cmd,envs:{TESTAGRAM_WEB_IMAGE:$web_image,TESTAGRAM_AUTH_JWT_SECRET:$auth_jwt_secret,AUTH_PUBLIC_BASE_URL:$auth_public_base_url,CLOUDFLARE_EMAIL_ACCOUNT_ID:$cloudflare_email_account_id,CLOUDFLARE_EMAIL_API_TOKEN:$cloudflare_email_api_token,AUTH_FROM_EMAIL:$auth_from_email,TESTAGRAM_MAIL_ENCRYPTION_KEY:$mail_encryption_key,FEDERATION_BACKEND_INTERNAL_TOKEN:$federation_token,EDGE_PROXY_SHARED_TOKEN:$edge_proxy_token,DATA_CENTRE_MEDIA_ENABLED:$media_enabled,MEDIA_REPLICATION_ENABLED:$media_replication_enabled,FRESH_MEDIA_ENABLED:$fresh_media_enabled,CONFIRM_NATIVE_IDENTITY_MIGRATIONS:$identity_migration_confirmation,TESTAGRAM_TERMS_VERSION:$terms_version,TESTAGRAM_PRIVACY_VERSION:$privacy_version,MEDIA_MODERATION_TOKEN:$moderation_token,MPESA_ENV:$mpesa_env,MPESA_SHORTCODE:$mpesa_shortcode,MPESA_PASSKEY:$mpesa_passkey,MPESA_CONSUMER_KEY:$mpesa_consumer_key,MPESA_CONSUMER_SECRET:$mpesa_consumer_secret,MPESA_CALLBACK_URL:$mpesa_callback,ADMIN_SERVICE_AUTH_KEY:$admin_service_key,B2_S3_ENDPOINT:$b2_endpoint,B2_S3_REGION:$b2_region,B2_S3_BUCKET:$b2_bucket,B2_S3_PRIVATE_BUCKET:$b2_private,B2_S3_PUBLIC_BUCKET:$b2_public,B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET:$b2_identity,B2_S3_ACCESS_KEY_ID:$b2_access,B2_S3_SECRET_ACCESS_KEY:$b2_secret,CLOUDFLARE_R2_ENDPOINT:$r2_endpoint,CLOUDFLARE_R2_REGION:$r2_region,CLOUDFLARE_R2_BUCKET:$r2_bucket,CLOUDFLARE_R2_PRIVATE_BUCKET:$r2_private,CLOUDFLARE_R2_PUBLIC_BUCKET:$r2_public,CLOUDFLARE_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET:$r2_identity,CLOUDFLARE_R2_METADATA_BUCKET:$r2_metadata_bucket,CLOUDFLARE_R2_ACCESS_KEY_ID:$r2_access,CLOUDFLARE_R2_SECRET_ACCESS_KEY:$r2_secret},timeoutSeconds:90}')"
response="$(curl --retry 3 --retry-all-errors --retry-delay 2 -fsS -X POST \
  -H "X-API-Key: $INSTABOX_API_KEY" \
  -H "Content-Type: application/json" \
  -d "$payload" "$API/v1/machines/$MACHINE_ID/exec")"
code="$(jq -r '.exitCode // 1' <<<"$response")"
if [[ "$code" != "0" ]]; then
  echo "::error::Could not start remote deployment command."
  jq -r '.stderr, .stdout' <<<"$response"
  exit 1
fi
echo "Remote deployment started."

