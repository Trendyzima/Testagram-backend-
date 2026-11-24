#!/usr/bin/env bash
set -euo pipefail

API_BASE="https://api.cloudflare.com/client/v4"
ZONE_NAME="testagram.site"
BOUNCE_DOMAIN="bounces.testagram.site"
BOUNCE_ADDRESS="b@bounces.testagram.site"
INGRESS_URL="https://api.testagram.site/internal/mail/bounce"
WORKER_DIR="${1:?worker directory is required}"

: "${CLOUDFLARE_BOUNCE_WORKER_API_TOKEN:?Missing scoped Cloudflare bounce-worker API token}"
: "${CLOUDFLARE_ACCOUNT_ID:?Missing Cloudflare account ID}"
: "${TESTAGRAM_MAIL_BOUNCE_INGRESS_SECRET:?Missing InstaBox-generated bounce ingress secret}"
[[ "${#TESTAGRAM_MAIL_BOUNCE_INGRESS_SECRET}" -ge 32 ]] || { echo "::error::Bounce ingress secret is too short."; exit 1; }
command -v sudo >/dev/null 2>&1 && sudo docker info >/dev/null 2>&1 || { echo "::error::Docker is required to run the pinned Wrangler deployment container on InstaBox."; exit 1; }

tmp="$(mktemp -d)"
auth_header="Authorization: Bearer $CLOUDFLARE_BOUNCE_WORKER_API_TOKEN"
script_subdomain_url="$API_BASE/accounts/$CLOUDFLARE_ACCOUNT_ID/workers/scripts/testagram-bounce-ingress/subdomain"
worker_probe_enabled=false
cleanup_worker_probe() {
  local exit_code=$?
  if [[ "$worker_probe_enabled" == true ]]; then
    local payload
    payload="$(jq -nc '{enabled:false,previews_enabled:false}')"
    if ! curl -fsS --connect-timeout 5 --max-time 15 -X POST "$script_subdomain_url" -H "$auth_header" -H 'Content-Type: application/json' --data "$payload" >/dev/null; then
      echo "::error::Automatic cleanup could not disable the temporary workers.dev URL; disable the testagram-bounce-ingress subdomain and previews in Cloudflare." >&2
      [[ "$exit_code" -ne 0 ]] || exit_code=1
    fi
  fi
  rm -rf "$tmp"
  return "$exit_code"
}
trap cleanup_worker_probe EXIT
# Recover safely if a previous interrupted rollout left the temporary
# workers.dev or preview endpoint enabled for this script.
preexisting_subdomain_status="$(curl -sS --connect-timeout 5 --max-time 15 -o "$tmp/preexisting-subdomain.json" -w '%{http_code}' "$script_subdomain_url" -H "$auth_header" || true)"
if [[ "$preexisting_subdomain_status" == 200 ]] && jq -e '.success == true and (.result.enabled == true or .result.previews_enabled == true)' "$tmp/preexisting-subdomain.json" >/dev/null; then
  worker_probe_enabled=true
fi

fetch_all_email_routing_rules() {
  local output="$1" page=1 pages=1 status
  : > "$tmp/email-routing-rules.ndjson"
  while (( page <= pages )); do
    status="$(curl -sS --connect-timeout 5 --max-time 20 -o "$tmp/email-routing-rules-page.json" -w '%{http_code}' "$API_BASE/zones/$zone_id/email/routing/rules?page=$page&per_page=100" -H "$auth_header")"
    if [[ "$status" != 200 ]] || ! jq -e '.success == true' "$tmp/email-routing-rules-page.json" >/dev/null; then
      return 1
    fi
    jq -c '.result[]?' "$tmp/email-routing-rules-page.json" >> "$tmp/email-routing-rules.ndjson"
    pages="$(jq -r '.result_info.total_pages // 1' "$tmp/email-routing-rules-page.json")"
    [[ "$pages" =~ ^[0-9]+$ ]] || return 1
    ((page++))
  done
  jq -s '{success:true,result:.}' "$tmp/email-routing-rules.ndjson" > "$output"
}


# Prove the public API route reaches the new HMAC-protected handler before
# registering an email route that could begin delivering real bounce traffic.
probe_status="$(curl -sS --connect-timeout 5 --max-time 15 -D "$tmp/api-probe.headers" -o "$tmp/api-probe.body" -w '%{http_code}' -X POST "$INGRESS_URL" -H 'Content-Type: message/rfc822' --data-binary '')"
if [[ "$probe_status" != 401 ]] || ! grep -qi '^X-Testagram-Bounce-Endpoint:[[:space:]]*native-v1' "$tmp/api-probe.headers"; then
  echo "::error::Testagram bounce API probe did not reach the native handler (expected HTTP 401 and X-Testagram-Bounce-Endpoint: native-v1; received HTTP $probe_status)."
  exit 1
fi

zones_status="$(curl -sS --connect-timeout 5 --max-time 20 -o "$tmp/zones.json" -w '%{http_code}' "$API_BASE/zones?name=$ZONE_NAME&status=active" -H "$auth_header")"
[[ "$zones_status" == 200 ]] || { echo "::error::Cloudflare zone lookup failed (HTTP $zones_status)."; exit 1; }
zone_id="$(jq -r --arg name "$ZONE_NAME" '[.result[]? | select(.name == $name)][0].id // empty' "$tmp/zones.json")"
[[ "$zone_id" =~ ^[A-Fa-f0-9]{32}$ ]] || { echo "::error::Cloudflare did not return exactly one active Testagram zone ID."; exit 1; }

# Subaddressing is a zone-wide Email Routing setting. Never toggle it
# automatically because that would change matching behavior for existing
# apex and subdomain routes. Require the operator to enable it after review.
settings_status="$(curl -sS --connect-timeout 5 --max-time 20 -o "$tmp/settings.json" -w '%{http_code}' "$API_BASE/zones/$zone_id/email/routing" -H "$auth_header")"
if [[ "$settings_status" != 200 ]] || ! jq -e '.success == true and .result.enabled == true and .result.support_subaddress == true' "$tmp/settings.json" >/dev/null; then
  echo "::error::Cloudflare Email Routing must be enabled and zone-wide subaddressing must already be true. Review existing routing rules before enabling plus-addressing in Cloudflare Email Routing Settings; this affects all rules, not only bounces.testagram.site."
  exit 1
fi

# The bounces.testagram.site Email Routing subdomain must first be enabled
# in Cloudflare Email Service > Email Routing > Settings > Subdomains.
# Verify its MX records before Wrangler is allowed to create a Worker route.
mx_ready=false
for attempt in $(seq 1 12); do
  mx_json="$(curl -fsS --connect-timeout 5 --max-time 10 -H 'accept: application/dns-json' --get --data-urlencode "name=$BOUNCE_DOMAIN" --data 'type=MX' https://cloudflare-dns.com/dns-query)" || mx_json='{}'
  if jq -e '(.Status == 0) and ([.Answer[]? | select(.type == 15)] as $mx | ($mx | length) >= 3 and all($mx[]; ((.data | ascii_downcase) | contains("mx.cloudflare.net"))))' <<<"$mx_json" >/dev/null; then
    mx_ready=true
    break
  fi
  sleep 5
done
if [[ "$mx_ready" != true ]]; then
  echo "::error::No Cloudflare Email Routing MX target is published for $BOUNCE_DOMAIN. Enable that subdomain in the dashboard; apex MX records are intentionally not changed."
  exit 1
fi

# Refuse to take over or shadow any pre-existing routing rule on this
# subdomain. In particular, explicit plus-address rules take precedence
# over the base address when subaddressing is enabled.
if ! fetch_all_email_routing_rules "$tmp/existing-rules.json"; then
  echo "::error::Could not inspect every existing Email Routing rule before Worker deployment."
  exit 1
fi
if ! jq -e --arg suffix "@$BOUNCE_DOMAIN" --arg address "$BOUNCE_ADDRESS" --arg worker "testagram-bounce-ingress" '
  [.result[]? | select(any(.matchers[]?; .type == "literal" and ((.value | ascii_downcase) | endswith($suffix))))] as $domain_rules
  | ($domain_rules | length) == 0 or
    (($domain_rules | length) == 1 and
     any($domain_rules[0].matchers[]?; .type == "literal" and .value == $address) and
     $domain_rules[0].source == "wrangler" and
     any($domain_rules[0].actions[]?; .type == "worker" and ((.value // []) | index($worker) != null)))
' "$tmp/existing-rules.json" >/dev/null; then
  echo "::error::A conflicting Email Routing rule exists on $BOUNCE_DOMAIN. Only the existing Wrangler-managed $BOUNCE_ADDRESS rule targeting testagram-bounce-ingress may be updated automatically."
  exit 1
fi
existing_bounce_rule_count="$(jq --arg suffix "@$BOUNCE_DOMAIN" '[.result[]? | select(any(.matchers[]?; .type == "literal" and ((.value | ascii_downcase) | endswith($suffix))))] | length' "$tmp/existing-rules.json")"

export CLOUDFLARE_API_TOKEN="$CLOUDFLARE_BOUNCE_WORKER_API_TOKEN"
WORKER_DIR="$(cd "$WORKER_DIR" && pwd)"
WRANGLER_IMAGE="node:22-bookworm-slim"
WRANGLER_NPM_CACHE="${HOME:-/root}/.cache/testagram-wrangler-npm"
mkdir -p "$WRANGLER_NPM_CACHE"
sudo docker pull "$WRANGLER_IMAGE"
run_wrangler() {
  sudo docker run --rm -i \
    --env CLOUDFLARE_API_TOKEN \
    --env CLOUDFLARE_ACCOUNT_ID \
    --mount "type=bind,source=$WORKER_DIR,target=/worker" \
    --mount "type=bind,source=$WRANGLER_NPM_CACHE,target=/root/.npm" \
    --workdir /worker \
    "$WRANGLER_IMAGE" \
    sh -lc 'npm exec --yes --package=wrangler@4.113.0 -- wrangler "$@"' sh "$@"
}
cd "$WORKER_DIR"

# The account-level workers.dev name is used only for the authenticated
# health probe. The per-script HTTP route is disabled again after verification.
workers_subdomain_status="$(curl -sS --connect-timeout 5 --max-time 20 -o "$tmp/workers-subdomain.json" -w '%{http_code}' "$API_BASE/accounts/$CLOUDFLARE_ACCOUNT_ID/workers/subdomain" -H "$auth_header")"
workers_subdomain="$(jq -r '.result.subdomain // empty' "$tmp/workers-subdomain.json")"
if [[ "$workers_subdomain_status" != 200 || ! "$workers_subdomain" =~ ^[a-z0-9-]+$ ]]; then
  echo "::error::A Cloudflare workers.dev account subdomain is required for the temporary authenticated Worker health probe. Configure it under Workers & Pages, then retry."
  exit 1
fi

# First install only: create the script and install its secret before adding
# the inbound address. On later deploys, preserve the existing email rule.
if [[ "$existing_bounce_rule_count" == "0" ]]; then
  run_wrangler deploy --config wrangler-bootstrap.toml
  worker_probe_enabled=true
  printf '%s' "$TESTAGRAM_MAIL_BOUNCE_INGRESS_SECRET" | run_wrangler secret put TESTAGRAM_BOUNCE_INGRESS_SECRET --config wrangler-bootstrap.toml
fi

# The probe config keeps the existing email address rule intact while briefly
# enabling workers.dev. The HMAC secret is installed before the health call.
run_wrangler deploy --config wrangler-probe.toml
worker_probe_enabled=true
printf '%s' "$TESTAGRAM_MAIL_BOUNCE_INGRESS_SECRET" | run_wrangler secret put TESTAGRAM_BOUNCE_INGRESS_SECRET --config wrangler-probe.toml

health_timestamp="$(date +%s)"
health_signature="$(python3 - "$health_timestamp" <<'PY'
import hashlib
import hmac
import os
import sys
secret, timestamp = os.environ["TESTAGRAM_MAIL_BOUNCE_INGRESS_SECRET"], sys.argv[1]
print(hmac.new(secret.encode(), ("health\n" + timestamp).encode(), hashlib.sha256).hexdigest())
PY
)"
worker_health_url="https://testagram-bounce-ingress.$workers_subdomain.workers.dev/__health"
worker_health_status="$(curl -sS --connect-timeout 5 --max-time 20 -o "$tmp/worker-health.txt" -w '%{http_code}' -X POST "$worker_health_url" -H "User-Agent: Mozilla/5.0 TestagramBounceHealth/1.0" -H "X-Testagram-Health-Timestamp: $health_timestamp" -H "X-Testagram-Health-Signature: $health_signature")"
if [[ "$worker_health_status" != 204 ]]; then
  echo "::error::The deployed Cloudflare Worker could not verify the public Testagram bounce API endpoint (expected authenticated 204 health result, received HTTP $worker_health_status)."
  exit 1
fi

# Only after the Worker-to-API probe passes do we deploy the final
# email-only configuration: workers.dev disabled, inbound address enabled.
run_wrangler deploy --config wrangler.toml
printf '%s' "$TESTAGRAM_MAIL_BOUNCE_INGRESS_SECRET" | run_wrangler secret put TESTAGRAM_BOUNCE_INGRESS_SECRET --config wrangler.toml

# Verify that the temporary workers.dev endpoint is disabled after the probe.
script_subdomain_status="$(curl -sS --connect-timeout 5 --max-time 20 -o "$tmp/script-subdomain.json" -w '%{http_code}' "$script_subdomain_url" -H "$auth_header")"
if [[ "$script_subdomain_status" == 200 ]] && jq -e '.success == true and (.result.enabled == true or .result.previews_enabled == true)' "$tmp/script-subdomain.json" >/dev/null; then
  disable_subdomain_payload="$(jq -nc '{enabled:false,previews_enabled:false}')"
  disable_subdomain_status="$(curl -sS --connect-timeout 5 --max-time 20 -o "$tmp/disable-subdomain.json" -w '%{http_code}' -X POST "$script_subdomain_url" -H "$auth_header" -H 'Content-Type: application/json' --data "$disable_subdomain_payload")"
  if [[ "$disable_subdomain_status" != 200 ]] || ! jq -e '.success == true and .result.enabled == false and .result.previews_enabled == false' "$tmp/disable-subdomain.json" >/dev/null; then
    echo "::error::Could not disable the temporary workers.dev and preview URLs."
    exit 1
  fi
fi
script_subdomain_status="$(curl -sS --connect-timeout 5 --max-time 20 -o "$tmp/script-subdomain.json" -w '%{http_code}' "$script_subdomain_url" -H "$auth_header")"
if [[ "$script_subdomain_status" != 200 ]] || ! jq -e '.success == true and .result.enabled == false and .result.previews_enabled == false' "$tmp/script-subdomain.json" >/dev/null; then
  echo "::error::The temporary workers.dev or preview URL remains enabled after cleanup."
  exit 1
fi
worker_probe_enabled=false

# Verify the route exists and targets a Worker before declaring setup complete.
if ! fetch_all_email_routing_rules "$tmp/rules.json" || ! jq -e --arg address "$BOUNCE_ADDRESS" --arg worker "testagram-bounce-ingress" '[.result[]? | select(any(.matchers[]?; .type == "literal" and .value == $address))] as $matches | ($matches | length) == 1 and $matches[0].source == "wrangler" and any($matches[0].actions[]?; .type == "worker" and ((.value // []) | index($worker) != null))' "$tmp/rules.json" >/dev/null; then
  echo "::error::Cloudflare did not confirm exactly one Worker routing rule for $BOUNCE_ADDRESS."
  exit 1
fi
echo "Verified Cloudflare Worker-to-API HTTPS reachability, disabled the temporary workers.dev probe URL, and installed the inbound-only bounce route and HMAC secret."
