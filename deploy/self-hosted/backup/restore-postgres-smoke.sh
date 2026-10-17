#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 || ! -s "$1" ]]; then
  echo "Usage: bash restore-postgres-smoke.sh /path/to/postgres.dump" >&2
  exit 2
fi
command -v docker >/dev/null

dump="$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"
name="testagram-restore-smoke-$$"
password="$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')"
cleanup() {
  docker rm -f "$name" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker run -d --rm --name "$name" --network none \
  -e POSTGRES_PASSWORD="$password" -e POSTGRES_DB=testagram \
  postgres:17-alpine >/dev/null

ready=0
for attempt in $(seq 1 40); do
  if docker exec "$name" pg_isready -U postgres -d testagram >/dev/null 2>&1; then ready=1; break; fi
  sleep 2
done
if [[ "$ready" != 1 ]]; then
  docker logs "$name" >&2 || true
  echo "Disposable PostgreSQL did not become ready." >&2
  exit 1
fi

docker cp "$dump" "$name:/tmp/postgres.dump" >/dev/null
docker exec "$name" pg_restore --no-owner --no-acl --exit-on-error -U postgres -d testagram /tmp/postgres.dump
result="$(docker exec "$name" psql -U postgres -d testagram -Atc "select to_regclass('public.vps_nodes') is not null and to_regclass('public.vps_workloads') is not null and to_regclass('testagram_hosting.domains') is not null and to_regclass('testagram_hosting.recovery_drills') is not null")"
if [[ "$result" != "t" ]]; then
  echo "Restore completed but required hosting tables are missing." >&2
  exit 1
fi
echo "PostgreSQL restore smoke test: PASS (isolated disposable container, no published port)."
