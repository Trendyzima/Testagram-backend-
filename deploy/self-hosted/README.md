# Testagram self-hosted platform stack

## Private platform service

The stack includes `platform:8095` for internal cache, rate limiting, queues, feature configuration and audit/event APIs. Its port is not published to the host; it is reachable only on the private Docker networks. The `PLATFORM_INTERNAL_TOKEN` in `.env` is required and must never be exposed to browser or mobile clients. Prometheus scrapes platform metrics on a separate internal monitoring network.

A fresh PostgreSQL data directory runs `migrations/001_platform.sql` through the PostgreSQL image's init hook. That hook does not modify an existing volume: apply the migration manually to an existing database as documented in `services/platform/README.md`, after taking a verified backup.

The platform client is available at `internal/platformclient`, but application call sites are not migrated yet. Keep this service optional until the consumer integration and end-to-end tests pass.


This is a local-first starting point for replacing managed infrastructure where
owned hardware and connectivity permit. It provisions PostgreSQL, Valkey
(Redis-compatible), private S3-compatible object storage, an Nginx media cache,
Caddy HTTPS reverse proxy, the existing Go capability gateway, and Prometheus.
Persistent data lives in local Docker volumes.

## Current boundaries — read before production use

- Valkey is used by the private platform service for cache, queues and rate limiting. The public capability gateway and product application call sites do not yet consume those APIs, so product-level cache/queue/rate-limit behavior is not automatically active.
- MinIO provisions separate private and public media buckets. Only objects intentionally
  uploaded to the public-media bucket are anonymously downloadable; the private
  bucket remains private. The current gateway does not yet upload media to MinIO
  or sign private object URLs, so the app integration is still required.
- Nginx caches GET/HEAD media requests only. This is a single-machine cache,
  not a global edge network. The application must publish media through this
  origin before media delivery will work.
- PostgreSQL is local and durable, but this does not migrate the existing
  Supabase schema, authentication, RLS policies, storage objects, or Edge
  Functions. The capability gateway still expects a compatible Supabase API
  configured using SUPABASE_URL and SUPABASE_PUBLISHABLE_KEY.
- Caddy can obtain free TLS certificates once public DNS points at this host
  and inbound ports 80/443 are reachable.
- Prometheus is private to the Compose network. Do not expose it publicly
  without an authenticated admin/VPN route.

## Start locally

1. Install Docker Engine and Docker Compose on the host.
2. Copy .env.example to .env. Replace every REPLACE_ placeholder with a
   unique long random secret, set a real operations email, then run chmod 600 .env.
3. Point api.testagram.site and media.testagram.site A/AAAA records to the
   host's public address. Publish AAAA only if IPv6 connectivity works.
4. Forward TCP ports 80 and 443 through the router/firewall. Never forward
   PostgreSQL, Valkey, MinIO, or Prometheus ports to the public internet.
5. From this directory run docker compose config --quiet, then
   docker compose up -d --build.
6. Verify https://api.testagram.site/healthz, /readyz, and /metrics. The
   media endpoint needs authorized object upload/signing integration before it
   can serve application media.
7. Inspect docker compose ps and logs before any production traffic cutover.

## Backup and recovery

Named Docker volumes are not backups. Before production use, implement and
test encrypted PostgreSQL dumps, MinIO replication/versioning, Valkey
rebuildability, off-machine backup copies, disk-space alerts, and power-loss
recovery. Keep at least one backup off the primary host: a single machine
cannot survive its own disk failure, theft, fire, or power loss.

## Zero-rent target

Run the app, cache, database, object store, and media cache on owned hardware;
use open-source software, private internal networking, free ACME certificates,
and automated builds, updates, backups, health checks, alerts, and restore
tests. Later, split roles across replaceable nodes and add private networking,
replicated storage, database replication/partitioning, and multiple edge nodes.

Zero rent does not mean zero cost. Domain renewals, electricity, internet
transit, static/public IP or tunnel, hardware replacement, cooling, and
off-site backups may still cost money. Residential ISP terms, CGNAT, upload
bandwidth, uptime, and power reliability can block home hosting. This is not
proof of 500-million-user capacity; that requires representative load tests,
multi-site redundancy, and an independently validated capacity design.
