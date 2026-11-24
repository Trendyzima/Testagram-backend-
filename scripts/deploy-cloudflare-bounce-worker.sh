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

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
auth_header="Authorization: Bearer $CLOUDFLARE_BOUNCE_WORKER_API_TOKEN"

# Prove the public API route reaches the new HMAC-protected handler before
# registering an email route that could begin delivering real bounce traffic.
probe_status="$(curl -sS --connect-timeout 5 --max-time 15 -o "$tmp/api-probe" -w '%{http_code}' -X POST "$INGRESS_URL" -H 'Content-Type: message/rfc822' --data-binary '')"
if [[ "$probe_status" != 401 ]]; then
  echo "::error::Testagram bounce API probe expected HTTP 401 without HMAC headers; received HTTP $probe_status."
  exit 1
fi

zones_status="$(curl -sS --connect-timeout 5 --max-time 20 -o "$tmp/zones.json" -w '%{http_code}' "$API_BASE/zones?name=$ZONE_NAME&status=active" -H "$auth_header")"
[[ "$zones_status" == 200 ]] || { echo "::error::Cloudflare zone lookup failed (HTTP $zones_status)."; exit 1; }
zone_id="$(jq -r --arg name "$ZONE_NAME" '[.result[]? | select(.name == $name)][0].id // empty' "$tmp/zones.json")"
[[ "$zone_id" =~ ^[A-Fa-f0-9]{32}$ ]] || { echo "::error::Cloudflare did not return exactly one active Testagram zone ID."; exit 1; }

# Enable plus-address matching without changing the zone's Email Routing
# enabled state, apex MX records, or any existing routing rules.
settings_status="$(curl -sS --connect-timeout 5 --max-time 20 -o "$tmp/settings.json" -w '%{http_code}' -X PATCH "$API_BASE/zones/$zone_id/email/routing" -H "$auth_header" -H 'Content-Type: application/json' --data-binary '{"support_subaddress":true}')"
if [[ "$settings_status" != 200 ]] || ! jq -e '.success == true and .result.support_subaddress == true' "$tmp/settings.json" >/dev/null; then
  echo "::error::Could not enable Cloudflare Email Routing subaddressing (HTTP $settings_status)."
  jq -c '{success,errors:[.errors[]? | {code,message}]}' "$tmp/settings.json" || true
  exit 1
fi

# The bounces.testagram.site Email Routing subdomain must first be enabled
# in Cloudflare Email Service > Email Routing > Settings > Subdomains.
# Verify its MX records before Wrangler is allowed to create a Worker route.
mx_ready=false
for attempt in $(seq 1 12); do
  mx_json="$(curl -fsS --connect-timeout 5 --max-time 10 -H 'accept: application/dns-json' --get --data-urlencode "name=$BOUNCE_DOMAIN" --data 'type=MX' https://cloudflare-dns.com/dns-query)" || mx_json='{}'
  if jq -e '(.Status == 0) and any(.Answer[]?; .type == 15 and ((.data | ascii_downcase) | contains("mx.cloudflare.net")))' <<<"$mx_json" >/dev/null; then
    mx_ready=true
    break
  fi
  sleep 5
done
if [[ "$mx_ready" != true ]]; then
  echo "::error::No Cloudflare Email Routing MX target is published for $BOUNCE_DOMAIN. Enable that subdomain in the dashboard; apex MX records are intentionally not changed."
  exit 1
fi

export CLOUDFLARE_API_TOKEN="$CLOUDFLARE_BOUNCE_WORKER_API_TOKEN"
cd "$WORKER_DIR"
npm exec --yes --package=wrangler@4.113.0 -- wrangler deploy --config wrangler.toml

# Transfer the InstaBox-generated secret directly to Cloudflare over the
# Wrangler API. It is never printed, placed in Git, or copied to the runner.
printf '%s' "$TESTAGRAM_MAIL_BOUNCE_INGRESS_SECRET" | npm exec --yes --package=wrangler@4.113.0 -- wrangler secret put TESTAGRAM_BOUNCE_INGRESS_SECRET --config wrangler.toml

# Verify the route exists and targets a Worker before declaring setup complete.
rules_status="$(curl -sS --connect-timeout 5 --max-time 20 -o "$tmp/rules.json" -w '%{http_code}' "$API_BASE/zones/$zone_id/email/routing/rules" -H "$auth_header")"
if [[ "$rules_status" != 200 ]] || ! jq -e --arg address "$BOUNCE_ADDRESS" '[.result[]? | select(any(.matchers[]?; .type == "literal" and .value == $address) and any(.actions[]?; .type == "worker"))] | length == 1' "$tmp/rules.json" >/dev/null; then
  echo "::error::Cloudflare did not confirm exactly one Worker routing rule for $BOUNCE_ADDRESS."
  exit 1
fi
echo "Verified Cloudflare inbound bounce route, plus-addressing, Testagram HTTPS endpoint, and Worker HMAC secret handoff."
