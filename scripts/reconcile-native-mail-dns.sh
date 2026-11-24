#!/usr/bin/env bash
# LEGACY DEDICATED-HOST RECONCILER ONLY. Do not run this on InstaBox: it
# owns the bounce MX for a separate public SMTP host. InstaBox must use
# scripts/reconcile-instabox-native-mail-dns.sh, which preserves Cloudflare MX.
# Reconcile only the dedicated native-mail DNS names in the authoritative Cloudflare zone.
# The caller supplies a narrowly scoped Zone:DNS:Edit token; no root MX records are touched.
set -Eeuo pipefail

: "${CLOUDFLARE_DNS_API_TOKEN:?CLOUDFLARE_DNS_API_TOKEN is required}"
: "${DKIM_PUBLIC_KEY:?DKIM_PUBLIC_KEY must be the public key generated on the dedicated SMTP host}"
API="https://api.cloudflare.com/client/v4"
DOMAIN="testagram.site"
MAIL_HOST="mail.testagram.site"
BOUNCE_HOST="bounces.testagram.site"
SELECTOR="tg2026._domainkey.testagram.site"
DMARC_HOST="_dmarc.testagram.site"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT
chmod 700 "$TMP_DIR"
umask 077
printf 'header = "Authorization: Bearer %s"\n' "$CLOUDFLARE_DNS_API_TOKEN" > "$TMP_DIR/cloudflare.curlrc"
chmod 600 "$TMP_DIR/cloudflare.curlrc"

api_get() {
  local url="$1" output="$2" status
  status="$(curl --silent --show-error --connect-timeout 10 --max-time 45 \
    -o "$output" -w '%{http_code}' --config "$TMP_DIR/cloudflare.curlrc" \
    -H 'Content-Type: application/json' "$url")"
  [[ "$status" == 200 ]] || { echo "Cloudflare API GET failed (HTTP $status)." >&2; return 1; }
  jq -e '.success == true' "$output" >/dev/null || { echo "Cloudflare API returned an unsuccessful response." >&2; return 1; }
}

api_write() {
  local method="$1" url="$2" body="$3" output="$4" status
  status="$(curl --silent --show-error --connect-timeout 10 --max-time 45 \
    -o "$output" -w '%{http_code}' -X "$method" \
    --config "$TMP_DIR/cloudflare.curlrc" \
    -H 'Content-Type: application/json' --data "$body" "$url")"
  [[ "$status" == 200 || "$status" == 201 ]] || { echo "Cloudflare DNS write failed (HTTP $status)." >&2; jq -c '{errors:[.errors[]? | {code,message}]}' "$output" >&2 || true; return 1; }
  jq -e '.success == true' "$output" >/dev/null || { echo "Cloudflare rejected a DNS record change." >&2; jq -c '{errors:[.errors[]? | {code,message}]}' "$output" >&2 || true; return 1; }
}

[[ "$DKIM_PUBLIC_KEY" =~ ^[A-Za-z0-9+/=]+$ ]] || { echo "Generated DKIM public key is not valid base64." >&2; exit 1; }
[[ ${#DKIM_PUBLIC_KEY} -ge 300 ]] || { echo "Generated DKIM public key is unexpectedly short." >&2; exit 1; }
# Never infer a mail-server address from an outbound HTTP request. On hosted
# sandbox platforms this can be a shared NAT egress IP that cannot receive SMTP.
# Run this reconciler only from the dedicated host that actually runs the SMTP
# listener, and provide its provider-assigned public IPv4 explicitly.
PUBLIC_IPV4="${TESTAGRAM_NATIVE_MAIL_PUBLIC_IPV4:-}"
if [[ -z "$PUBLIC_IPV4" ]]; then
  echo "TESTAGRAM_NATIVE_MAIL_PUBLIC_IPV4 is unset; refusing to publish native-mail DNS." >&2
  exit 1
fi
[[ "$PUBLIC_IPV4" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]] || { echo "The configured native-mail IPv4 address is malformed." >&2; exit 1; }
python3 - "$PUBLIC_IPV4" <<'PY'
import ipaddress,sys
ip=ipaddress.ip_address(sys.argv[1])
if ip.version != 4 or not ip.is_global:
    raise SystemExit("The configured mail address is not a globally routable IPv4 address.")
PY

api_get "$API/zones?name=$DOMAIN&status=active&per_page=50" "$TMP_DIR/zones.json"
ZONE_COUNT="$(jq '.result | length' "$TMP_DIR/zones.json")"
[[ "$ZONE_COUNT" == 1 ]] || { echo "Expected exactly one active Cloudflare zone for $DOMAIN; found $ZONE_COUNT." >&2; exit 1; }
ZONE_ID="$(jq -r '.result[0].id' "$TMP_DIR/zones.json")"
[[ "$ZONE_ID" =~ ^[a-f0-9]{32}$ ]] || { echo "Cloudflare returned an invalid zone identifier." >&2; exit 1; }

# Read every page before writing so conflicts and duplicate SPF/DMARC records fail closed.
: > "$TMP_DIR/records.jsonl"
page=1
while :; do
  api_get "$API/zones/$ZONE_ID/dns_records?per_page=100&page=$page" "$TMP_DIR/page.json"
  jq -c '.result[]' "$TMP_DIR/page.json" >> "$TMP_DIR/records.jsonl"
  total_pages="$(jq -r '.result_info.total_pages // 1' "$TMP_DIR/page.json")"
  [[ "$page" -lt "$total_pages" ]] || break
  ((page+=1))
done

upsert_record() {
  local type="$1" name="$2" content="$3" priority="${4:-}" proxied="${5:-false}"
  local matches count record_id body url method
  if [[ "$type" == "TXT" && "$name" == "$BOUNCE_HOST" ]]; then
    matches="$(jq -s --arg name "$name" '[.[] | select(.type=="TXT" and .name==$name and (.content | startswith("v=spf1")))]' "$TMP_DIR/records.jsonl")"
  elif [[ "$type" == "TXT" && "$name" == "$DMARC_HOST" ]]; then
    matches="$(jq -s --arg name "$name" '[.[] | select(.type=="TXT" and .name==$name and (.content | startswith("v=DMARC1")))]' "$TMP_DIR/records.jsonl")"
  else
    matches="$(jq -s --arg type "$type" --arg name "$name" '[.[] | select(.type==$type and .name==$name)]' "$TMP_DIR/records.jsonl")"
  fi
  count="$(jq 'length' <<<"$matches")"
  if [[ "$type" == "A" ]]; then
    local cname_count
    cname_count="$(jq -s --arg name "$name" '[.[] | select(.type=="CNAME" and .name==$name)] | length' "$TMP_DIR/records.jsonl")"
    [[ "$cname_count" == 0 ]] || { echo "Refusing to replace an existing CNAME at $name." >&2; return 1; }
  fi
  if [[ "$type" == "TXT" && ( "$name" == "$BOUNCE_HOST" || "$name" == "$DMARC_HOST" ) ]]; then
    local relevant_count
    if [[ "$name" == "$BOUNCE_HOST" ]]; then
      relevant_count="$(jq -s --arg name "$name" '[.[] | select(.type=="TXT" and .name==$name and (.content | startswith("v=spf1")))] | length' "$TMP_DIR/records.jsonl")"
    else
      relevant_count="$(jq -s --arg name "$name" '[.[] | select(.type=="TXT" and .name==$name and (.content | startswith("v=DMARC1")))] | length' "$TMP_DIR/records.jsonl")"
    fi
    [[ "$relevant_count" -le 1 ]] || { echo "Multiple policy TXT records exist at $name; refusing ambiguous reconciliation." >&2; return 1; }
  fi
  [[ "$count" -le 1 ]] || { echo "Multiple $type records exist at $name; refusing ambiguous reconciliation." >&2; return 1; }
  if [[ "$type" == "TXT" && "$name" == "$DMARC_HOST" && "$count" == 1 ]]; then
    echo "Preserving the existing DMARC policy at $name; no policy downgrade or reporting change was made."
    return 0
  fi
  if [[ "$type" == "MX" ]]; then
    body="$(jq -nc --arg type "$type" --arg name "$name" --arg content "$content" --argjson priority "$priority" '{type:$type,name:$name,content:$content,priority:$priority,ttl:1}')"
  elif [[ "$type" == "A" ]]; then
    body="$(jq -nc --arg type "$type" --arg name "$name" --arg content "$content" '{type:$type,name:$name,content:$content,ttl:1,proxied:false}')"
  else
    body="$(jq -nc --arg type "$type" --arg name "$name" --arg content "$content" '{type:$type,name:$name,content:$content,ttl:1}')"
  fi
  if [[ "$count" == 1 ]]; then
    record_id="$(jq -r '.[0].id' <<<"$matches")"
    url="$API/zones/$ZONE_ID/dns_records/$record_id"
    method="PUT"
  else
    url="$API/zones/$ZONE_ID/dns_records"
    method="POST"
  fi
  api_write "$method" "$url" "$body" "$TMP_DIR/write.json"
  echo "Reconciled $type $name."
  # Refresh every page so later checks never operate on a truncated snapshot.
  : > "$TMP_DIR/records.jsonl"
  refresh_page=1
  while :; do
    api_get "$API/zones/$ZONE_ID/dns_records?per_page=100&page=$refresh_page" "$TMP_DIR/refresh.json"
    jq -c '.result[]' "$TMP_DIR/refresh.json" >> "$TMP_DIR/records.jsonl"
    refresh_total_pages="$(jq -r '.result_info.total_pages // 1' "$TMP_DIR/refresh.json")"
    [[ "$refresh_page" -lt "$refresh_total_pages" ]] || break
    ((refresh_page+=1))
  done
}

upsert_record A "$MAIL_HOST" "$PUBLIC_IPV4"
upsert_record MX "$BOUNCE_HOST" "$MAIL_HOST" 10
upsert_record TXT "$BOUNCE_HOST" "v=spf1 ip4:$PUBLIC_IPV4 ~all"
upsert_record TXT "$SELECTOR" "v=DKIM1; k=rsa; p=$DKIM_PUBLIC_KEY"
upsert_record TXT "$DMARC_HOST" "v=DMARC1; p=none; adkim=r; aspf=r"

echo "Native mail DNS records reconciled. Public IPv4: $PUBLIC_IPV4"
echo "PTR/rDNS is a VPS-provider setting and is not changed by Cloudflare DNS."
