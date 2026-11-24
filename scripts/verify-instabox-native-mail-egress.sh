#!/usr/bin/env bash
set -euo pipefail

expected="${TESTAGRAM_NATIVE_MAIL_EXPECTED_EGRESS_IPV4:-}"
if [[ -z "$expected" ]]; then
  echo "::error::Set TESTAGRAM_NATIVE_MAIL_EXPECTED_EGRESS_IPV4 only after InstaBox confirms a stable, dedicated SMTP egress IPv4 and configurable PTR."
  exit 1
fi

ipify="$(curl -4fsS --connect-timeout 5 --max-time 15 https://api.ipify.org | tr -d '[:space:]')"
amazon="$(curl -4fsS --connect-timeout 5 --max-time 15 https://checkip.amazonaws.com | tr -d '[:space:]')"
python3 - "$expected" "$ipify" "$amazon" <<'PY'
import ipaddress
import socket
import sys

expected, ipify, amazon = sys.argv[1:]
try:
    addresses = [ipaddress.ip_address(value) for value in (expected, ipify, amazon)]
except ValueError as exc:
    raise SystemExit(f"Invalid IPv4 address in native mail egress check: {exc}")
if any(address.version != 4 or not address.is_global for address in addresses):
    raise SystemExit("Native mail egress identity must be a globally routable IPv4 address.")
if len({str(address) for address in addresses}) != 1:
    raise SystemExit("InstaBox SMTP egress is not consistent across independent IPv4 checks or does not match the provider-confirmed expected address.")
try:
    ptr = socket.gethostbyaddr(str(addresses[0]))[0].rstrip(".").lower()
except OSError as exc:
    raise SystemExit(f"No usable reverse-DNS PTR exists for the InstaBox SMTP egress IPv4: {exc}")
if ptr != "mail.testagram.site":
    raise SystemExit(f"SMTP egress PTR must be mail.testagram.site; observed {ptr!r}.")
print(f"Verified InstaBox SMTP egress IPv4: {addresses[0]}")
print(f"Verified PTR: {ptr}")
print("Forward A record will be reconciled only after this provider-confirmed egress identity passes.")
PY
