#!/usr/bin/env bash
set -euo pipefail

# Two independent IPv4 reflectors must agree. The reverse-DNS identity is
# independently checked; provider confirmation is still required to establish
# that the address is dedicated and static.
ipify="$(curl -4fsS --connect-timeout 5 --max-time 15 https://api.ipify.org | tr -d '[:space:]')"
amazon="$(curl -4fsS --connect-timeout 5 --max-time 15 https://checkip.amazonaws.com | tr -d '[:space:]')"
python3 - "$ipify" "$amazon" <<'PY'
import ipaddress
import socket
import sys

values = sys.argv[1:]
try:
    addresses = [ipaddress.ip_address(value) for value in values]
except ValueError as exc:
    raise SystemExit(f"Invalid public IPv4 observation: {exc}")
if len(addresses) != 2 or any(address.version != 4 or not address.is_global for address in addresses):
    raise SystemExit("Both independent InstaBox egress observations must be globally routable IPv4 addresses.")
if str(addresses[0]) != str(addresses[1]):
    raise SystemExit("Independent IPv4 reflectors disagree; refusing native-mail DNS reconciliation.")
try:
    ptr = socket.gethostbyaddr(str(addresses[0]))[0].rstrip(".").lower()
except OSError as exc:
    raise SystemExit(f"No usable reverse-DNS PTR exists for the InstaBox SMTP egress IPv4: {exc}")
if ptr != "mail.testagram.site":
    raise SystemExit(f"SMTP egress PTR must be mail.testagram.site; observed {ptr!r}.")
print(str(addresses[0]))
PY
