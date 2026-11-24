#!/usr/bin/env bash
# InstaBox direct-MX DNS reconciliation. Cloudflare owns the bounce MX; this
# script updates only the outbound identity A, SPF, DKIM and missing DMARC.
set -Eeuo pipefail

: "${CLOUDFLARE_DNS_API_TOKEN:?CLOUDFLARE_DNS_API_TOKEN is required}"
: "${TESTAGRAM_NATIVE_MAIL_EXPECTED_EGRESS_IPV4:?Provider-confirmed static SMTP egress IPv4 is required}"
: "${DKIM_PUBLIC_KEY:?Testagram-generated DKIM public key is required}"
API="https://api.cloudflare.com/client/v4"
DOMAIN="testagram.site"
MAIL_HOST="mail.testagram.site"
BOUNCE_HOST="bounces.testagram.site"
SELECTOR="tg2026._domainkey.testagram.site"
DMARC_HOST="_dmarc.testagram.site"
PUBLIC_IPV4="$TESTAGRAM_NATIVE_MAIL_EXPECTED_EGRESS_IPV4"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT
chmod 700 "$TMP_DIR"
umask 077
printf 'header = "Authorization: Bearer %s"\n' "$CLOUDFLARE_DNS_API_TOKEN" > "$TMP_DIR/cloudflare.curlrc"
chmod 600 "$TMP_DIR/cloudflare.curlrc"

python3 - "$PUBLIC_IPV4" <<'PY'
import ipaddress, sys
ip = ipaddress.ip_address(sys.argv[1])
if ip.version != 4 or not ip.is_global:
    raise SystemExit("SMTP egress address must be a globally routable IPv4.")
PY
[[ "$DKIM_PUBLIC_KEY" =~ ^[A-Za-z0-9+/=]+$ && ${#DKIM_PUBLIC_KEY} -ge 300 ]] || { echo "Invalid or unexpectedly short Testagram DKIM public key." >&2; exit 1; }

api_get() {
  local url="$1" output="$2" status
  status="$(curl --silent --show-error --connect-timeout 10 --max-time 45 -o "$output" -w '%{http_code}' --config "$TMP_DIR/cloudflare.curlrc" -H 'Content-Type: application/json' "$url")"
  [[ "$status" == 200 ]] && jq -e '.success == true' "$output" >/dev/null || { echo "Cloudflare DNS API GET failed (HTTP $status)." >&2; return 1; }
}
api_write() {
  local method="$1" url="$2" body="$3" output="$4" status
  status="$(curl --silent --show-error --connect-timeout 10 --max-time 45 -o "$output" -w '%{http_code}' -X "$method" --config "$TMP_DIR/cloudflare.curlrc" -H 'Content-Type: application/json' --data "$body" "$url")"
  [[ "$status" == 200 || "$status" == 201 ]] && jq -e '.success == true' "$output" >/dev/null || { echo "Cloudflare DNS write failed (HTTP $status)." >&2; jq -c '{errors:[.errors[]? | {code,message}]}' "$output" >&2 || true; return 1; }
}
api_get "$API/zones?name=$DOMAIN&status=active&per_page=50" "$TMP_DIR/zones.json"
zone_count="$(jq --arg name "$DOMAIN" '[.result[]? | select(.name == $name)] | length' "$TMP_DIR/zones.json")"
[[ "$zone_count" == 1 ]] || { echo "Expected exactly one active Cloudflare zone for $DOMAIN; found $zone_count." >&2; exit 1; }
zone_id="$(jq -r --arg name "$DOMAIN" '.result[] | select(.name == $name) | .id' "$TMP_DIR/zones.json")"
[[ "$zone_id" =~ ^[A-Fa-f0-9]{32}$ ]] || { echo "Invalid Cloudflare zone identifier." >&2; exit 1; }

get_records() {
  local type="$1" name="$2"
  api_get "$API/zones/$zone_id/dns_records?type=$type&name=$name&per_page=100" "$TMP_DIR/records.json"
  jq -c '.result // []' "$TMP_DIR/records.json"
}
write_record() {
  local type="$1" name="$2" content="$3" proxied="${4:-false}"
  local matches count record_id body url method
  matches="$(get_records "$type" "$name")"
  count="$(jq 'length' <<<"$matches")"
  [[ "$count" -le 1 ]] || { echo "Multiple $type records exist at $name; refusing ambiguous DNS reconciliation." >&2; return 1; }
  if [[ "$type" == "A" ]]; then
    local cnames
    cnames="$(get_records CNAME "$name" | jq 'length')"
    [[ "$cnames" == 0 ]] || { echo "Refusing to replace existing CNAME at $name." >&2; return 1; }
    body="$(jq -nc --arg type "$type" --arg name "$name" --arg content "$content" '{type:$type,name:$name,content:$content,ttl:1,proxied:false}')"
  else
    body="$(jq -nc --arg type "$type" --arg name "$name" --arg content "$content" '{type:$type,name:$name,content:$content,ttl:1}')"
  fi
  if [[ "$count" == 1 ]]; then
    record_id="$(jq -r '.[0].id' <<<"$matches")"
    url="$API/zones/$zone_id/dns_records/$record_id"
    method="PUT"
  else
    url="$API/zones/$zone_id/dns_records"
    method="POST"
  fi
  api_write "$method" "$url" "$body" "$TMP_DIR/write.json"
  echo "Reconciled $type $name."
}

# Cloudflare Email Routing owns these MX records. Never replace them with
# mail.testagram.site: InstaBox cannot receive inbound SMTP/25.
mx_json="$(curl -fsS --connect-timeout 5 --max-time 15 -H 'accept: application/dns-json' --get --data-urlencode "name=$BOUNCE_HOST" --data 'type=MX' https://cloudflare-dns.com/dns-query)"
jq -e '(.Status == 0) and any(.Answer[]?; .type == 15 and ((.data | ascii_downcase) | contains("mx.cloudflare.net")))' <<<"$mx_json" >/dev/null || { echo "Cloudflare Email Routing MX is not active for $BOUNCE_HOST; refusing to publish bounce-domain SPF." >&2; exit 1; }

# Publish the verified outbound host identity as DNS-only, never proxied.
write_record A "$MAIL_HOST" "$PUBLIC_IPV4"

# Merge, rather than duplicate, Cloudflare's Email Routing SPF mechanism
# with the InstaBox direct-MX source IP. Preserve the existing all qualifier.
spf_records="$(get_records TXT "$BOUNCE_HOST" | jq '[.[] | select(.content | startswith("v=spf1"))]')"
spf_count="$(jq 'length' <<<"$spf_records")"
[[ "$spf_count" -le 1 ]] || { echo "Multiple SPF records exist at $BOUNCE_HOST; refusing to publish an invalid SPF state." >&2; exit 1; }
if [[ "$spf_count" == 1 ]]; then
  existing_spf="$(jq -r '.[0].content' <<<"$spf_records")"
  merged_spf="$(python3 - "$existing_spf" "$PUBLIC_IPV4" <<'PY'
import re, sys
record, ip = sys.argv[1:]
parts = record.split()
if not parts or parts[0].lower() != "v=spf1":
    raise SystemExit("Existing SPF record is malformed.")
all_re = re.compile(r"^[+?~-]?all$", re.I)
all_terms = [part for part in parts[1:] if all_re.match(part)]
if len(all_terms) > 1:
    raise SystemExit("Existing SPF record has multiple all mechanisms.")
mechanisms = [part for part in parts[1:] if not all_re.match(part)]
ip_term = "ip4:" + ip
cf_term = "include:_spf.mx.cloudflare.net"
if ip_term not in mechanisms:
    mechanisms.append(ip_term)
if cf_term not in mechanisms:
    mechanisms.append(cf_term)
all_term = all_terms[0] if all_terms else "~all"
print(" ".join(["v=spf1", *mechanisms, all_term]))
PY
  )"
else
  merged_spf="v=spf1 ip4:$PUBLIC_IPV4 include:_spf.mx.cloudflare.net ~all"
fi
write_record TXT "$BOUNCE_HOST" "$merged_spf"

# Publish Testagram's own DKIM selector. Cloudflare's routing DKIM selector
# is separate and remains untouched.
write_record TXT "$SELECTOR" "v=DKIM1; k=rsa; p=$DKIM_PUBLIC_KEY"

# Preserve any existing DMARC policy exactly; create monitoring only if absent.
dmarc_records="$(get_records TXT "$DMARC_HOST" | jq '[.[] | select(.content | startswith("v=DMARC1"))]')"
dmarc_count="$(jq 'length' <<<"$dmarc_records")"
[[ "$dmarc_count" -le 1 ]] || { echo "Multiple DMARC records exist at $DMARC_HOST; refusing ambiguous policy." >&2; exit 1; }
if [[ "$dmarc_count" == 0 ]]; then
  write_record TXT "$DMARC_HOST" "v=DMARC1; p=none; adkim=r; aspf=r"
else
  echo "Preserved existing DMARC policy at $DMARC_HOST."
fi

# Read-after-write verification for every outbound authentication record.
actual_a="$(get_records A "$MAIL_HOST" | jq -r '.[0].content // empty')"
[[ "$actual_a" == "$PUBLIC_IPV4" ]] || { echo "mail.testagram.site A record does not match the verified egress IP." >&2; exit 1; }
actual_spf="$(get_records TXT "$BOUNCE_HOST" | jq -r '[.[] | select(.content | startswith("v=spf1"))] | if length == 1 then .[0].content else "" end')"
[[ "$actual_spf" == *"ip4:$PUBLIC_IPV4"* && "$actual_spf" == *"include:_spf.mx.cloudflare.net"* ]] || { echo "Bounce-domain SPF is missing the verified InstaBox IP or Cloudflare routing include." >&2; exit 1; }
actual_dkim="$(get_records TXT "$SELECTOR" | jq -r '.[0].content // empty')"
[[ "$actual_dkim" == "v=DKIM1; k=rsa; p=$DKIM_PUBLIC_KEY" ]] || { echo "Testagram DKIM TXT record failed read-after-write verification." >&2; exit 1; }
echo "Verified mail.testagram.site A, merged bounce SPF, Testagram DKIM selector, and preserved DMARC."
echo "Cloudflare Email Routing MX at $BOUNCE_HOST was checked and left unchanged."
