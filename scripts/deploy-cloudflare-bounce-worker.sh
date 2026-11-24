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
command -v node >/dev/null 2>&1 && command -v npm >/dev/null 2>&1 || { echo "::error::Node.js 18+ and npm are required to deploy the Cloudflare Worker."; exit 1; }
node_major="$(node -p 'process.versions.node.split(".")[0]')"
[[ "$node_major" =~ ^[0-9]+$ && "$node_major" -ge 18 ]] || { echo "::error::Cloudflare Wrangler requires Node.js 18 or later."; exit 1; }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
auth_header="Authorization: Bearer $CLOUDFLARE_BOUNCE_WORKER_API_TOKEN"

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
existing_rules_status="$(curl -sS --connect-timeout 5 --max-time 20 -o "$tmp/existing-rules.json" -w '%{http_code}' "$API_BASE/zones/$zone_id/email/routing/rules" -H "$auth_header")"
if [[ "$existing_rules_status" != 200 ]] || ! jq -e '.success == true' "$tmp/existing-rules.json" >/dev/null; then
  echo "::error::Could not inspect existing Email Routing rules before Worker deployment."
  exit 1
fi
if jq -e --arg suffix "@$BOUNCE_DOMAIN" '[.result[]? | select(any(.matchers[]?; .type == "literal" and ((.value | ascii_downcase) | endswith($suffix))))] | length > 0' "$tmp/existing-rules.json" >/dev/null; then
  echo "::error::A routing rule already exists on $BOUNCE_DOMAIN. Review it and remove or migrate it explicitly before Wrangler takes ownership."
  exit 1
fi

export CLOUDFLARE_API_TOKEN="$CLOUDFLARE_BOUNCE_WORKER_API_TOKEN"
cd "$WORKER_DIR"

# First deploy the same Worker temporarily on workers.dev, without registering
# the inbound email address. This lets us prove the Cloudflare runtime can
# reach the exact public Testagram API route before accepting bounce traffic.
workers_subdomain_status="$(curl -sS --connect-timeout 5 --max-time 20 -o "$tmp/workers-subdomain.json" -w '%{http_code}' "$API_BASE/accounts/$CLOUDFLARE_ACCOUNT_ID/workers/subdomain" -H "$auth_header")"
workers_subdomain="$(jq -r '.result.subdomain // empty' "$tmp/workers-subdomain.json")"
if [[ "$workers_subdomain_status" != 200 || ! "$workers_subdomain" =~ ^[a-z0-9-]+$ ]]; then
  echo "::error::A Cloudflare workers.dev account subdomain is required for the temporary authenticated Worker health probe. Configure it under Workers & Pages, then retry."
  exit 1
fi
npm exec --yes --package=wrangler@4.113.0 -- wrangler deploy --config wrangler-probe.toml

# Transfer the InstaBox-generated secret directly to Cloudflare over the
# Wrangler API. It is never printed, placed in Git, or copied to the runner.
printf '%s' "$TESTAGRAM_MAIL_BOUNCE_INGRESS_SECRET" | npm exec --yes --package=wrangler@4.113.0 -- wrangler secret put TESTAGRAM_BOUNCE_INGRESS_SECRET --config wrangler-probe.toml

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
npm exec --yes --package=wrangler@4.113.0 -- wrangler deploy --config wrangler.toml
printf '%s' "$TESTAGRAM_MAIL_BOUNCE_INGRESS_SECRET" | npm exec --yes --package=wrangler@4.113.0 -- wrangler secret put TESTAGRAM_BOUNCE_INGRESS_SECRET --config wrangler.toml

# Verify that the temporary workers.dev endpoint is disabled after the probe.
script_subdomain_url="$API_BASE/accounts/$CLOUDFLARE_ACCOUNT_ID/workers/scripts/testagram-bounce-ingress/subdomain"
script_subdomain_status="$(curl -sS --connect-timeout 5 --max-time 20 -o "$tmp/script-subdomain.json" -w '%{http_code}' "$script_subdomain_url" -H "$auth_header")"
if [[ "$script_subdomain_status" == 200 ]] && jq -e '.success == true and .result.enabled == true' "$tmp/script-subdomain.json" >/dev/null; then
  delete_subdomain_status="$(curl -sS --connect-timeout 5 --max-time 20 -o "$tmp/delete-subdomain.json" -w '%{http_code}' -X DELETE "$script_subdomain_url" -H "$auth_header")"
  if [[ "$delete_subdomain_status" != 200 ]] || ! jq -e '.success == true' "$tmp/delete-subdomain.json" >/dev/null; then
    echo "::error::Could not remove the temporary workers.dev health endpoint."
    exit 1
  fi
fi
script_subdomain_status="$(curl -sS --connect-timeout 5 --max-time 20 -o "$tmp/script-subdomain.json" -w '%{http_code}' "$script_subdomain_url" -H "$auth_header")"
if [[ "$script_subdomain_status" != 200 ]] || ! jq -e '.success == true and .result.enabled == false' "$tmp/script-subdomain.json" >/dev/null; then
  echo "::error::The temporary workers.dev health endpoint remains enabled after cleanup."
  exit 1
fi

# Verify the route exists and targets a Worker before declaring setup complete.
rules_status="$(curl -sS --connect-timeout 5 --max-time 20 -o "$tmp/rules.json" -w '%{http_code}' "$API_BASE/zones/$zone_id/email/routing/rules" -H "$auth_header")"
if [[ "$rules_status" != 200 ]] || ! jq -e --arg address "$BOUNCE_ADDRESS" --arg worker "testagram-bounce-ingress" '[.result[]? | select(any(.matchers[]?; .type == "literal" and .value == $address))] as $matches | ($matches | length) == 1 and $matches[0].source == "wrangler" and any($matches[0].actions[]?; .type == "worker" and ((.value // []) | index($worker) != null))' "$tmp/rules.json" >/dev/null; then
  echo "::error::Cloudflare did not confirm exactly one Worker routing rule for $BOUNCE_ADDRESS."
  exit 1
fi
echo "Verified Cloudflare Worker-to-API HTTPS reachability, disabled the temporary workers.dev probe URL, and installed the inbound-only bounce route and HMAC secret."
