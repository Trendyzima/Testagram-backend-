# Testagram self-hosted platform stack

Local-first PostgreSQL, Valkey, object storage, API gateway, media cache, HTTPS edge and monitoring for owned hardware.

## Operator dashboard

Grafana is provisioned alongside Prometheus. Use the `Testagram Operations` dashboard for API request rate, latency and error rates, cache performance, database pool pressure, configured queue backlog/age, and host CPU/memory/disk/network metrics.

Set `GRAFANA_ADMIN_USER` and a strong `GRAFANA_ADMIN_PASSWORD` in this directory's `.env` file before bringing up the stack. Grafana binds to `127.0.0.1:3000` only; connect from an operator workstation with `ssh -N -L 3000:127.0.0.1:3000 user@your-vps-host`, then browse to `http://127.0.0.1:3000`. Do not publish Grafana to a public interface.

Prometheus is wired to the bundled Alertmanager service. To enable outbound notifications, set `ALERTMANAGER_WEBHOOK_URL` in `deploy/self-hosted/.env` to an operator-controlled HTTP(S) webhook endpoint. Keep the URL secret, do not commit it, and verify the receiving system accepts Alertmanager's JSON payload. If the variable is blank, alerts still appear in Prometheus/Alertmanager but **no outbound notifications are sent**. This is intentional fail-closed behavior, not proof of on-call delivery.

After setting the webhook URL, recreate/restart the monitoring services so Alertmanager regenerates its config:

```bash
docker compose --env-file deploy/self-hosted/.env -f deploy/self-hosted/docker-compose.yml up -d --force-recreate alertmanager prometheus
docker compose --env-file deploy/self-hosted/.env -f deploy/self-hosted/docker-compose.yml logs --tail=100 alertmanager prometheus
```

Prometheus and Alertmanager web interfaces are loopback-only; use SSH tunnels if you need to inspect them remotely. Send a controlled test alert to verify the webhook receiver and confirm the receiving system delivers it. Alertmanager being healthy does not prove that a downstream webhook is reachable or that an operator received a message.

## Verify monitoring is actually working

After starting the self-hosted stack, check container health and scrape targets rather than assuming a visible dashboard means metrics are current:

```bash
docker compose --env-file deploy/self-hosted/.env -f deploy/self-hosted/docker-compose.yml ps
docker compose --env-file deploy/self-hosted/.env -f deploy/self-hosted/docker-compose.yml logs --tail=150 prometheus alertmanager grafana
```

Open Grafana through the SSH tunnel described above and select **Testagram Operations**. In Prometheus, the `up` query should return `1` for `testagram-api`, `testagram-platform`, `testagram-grafana`, `testagram-alertmanager`, `testagram-node`, and `prometheus`. A target value of `0` means Prometheus can see the target but cannot scrape it; a missing series usually means the target or scrape configuration is absent. Check **Status → Targets** and **Alerts** in Prometheus when a panel is empty.

CI validates both the Prometheus scrape configuration and alert-rule syntax with `promtool`. That does not prove runtime delivery: run a staging smoke test after changes and confirm expected panels receive samples.

**Release gate:** CI checks Alertmanager configuration syntax in both webhook-enabled and no-webhook modes. It does not test the real endpoint, message delivery, or on-call response; those require a staging notification test.

## Staff console

The stack now has an optional-to-configure, HTTPS-routed staff console on `ADMIN_DOMAIN` (default `ops.testagram.site`). Set `SUPABASE_URL`, `SUPABASE_PUBLISHABLE_KEY`, `ADMIN_PUBLIC_URL`, and a fresh 64-hex-character `ADMIN_SESSION_KEY` in `.env`. The invite feature additionally requires `SUPABASE_SERVICE_ROLE_KEY`; keep it server-only. Configure Supabase Auth allowed redirects for the console root and `/reset-password`, provision the first owner's `app_metadata.staff_role=owner` using a trusted admin operation, and point DNS at the VPS.

After configuration, run:

```bash
docker compose --env-file deploy/self-hosted/.env -f deploy/self-hosted/docker-compose.yml config --quiet
docker compose --env-file deploy/self-hosted/.env -f deploy/self-hosted/docker-compose.yml up -d --build admin-console caddy
```

Visit `https://ops.testagram.site`. The console's monitoring APIs are protected by server-side Auth role checks; user-editable metadata cannot grant staff access. New sign-ups remain pending until an owner-approved role is assigned. Grafana remains loopback-only and should be accessed through an SSH tunnel as described above. Full instructions and release gates are in [admin-console/README.md](../../admin-console/README.md).

## Private platform service

The stack includes `platform:8095` for internal cache, rate limiting, queues, feature configuration and audit/event APIs. Its port is not published to the host; it is reachable only on the private Docker networks. The `PLATFORM_INTERNAL_TOKEN` in `.env` is required and must never be exposed to browser or mobile clients. Prometheus scrapes platform metrics on a separate internal monitoring network.

A fresh PostgreSQL data directory runs `migrations/001_platform.sql` through the PostgreSQL image's init hook. That hook does not modify an existing volume: apply the migration manually to an existing database as documented in `services/platform/README.md`, after taking a verified backup.

The platform client is available at `internal/platformclient`, but application call sites are not migrated yet. Keep this service optional until the consumer integration and end-to-end tests pass.


This is a local-first starting point for replacing managed infrastructure where
owned hardware and connectivity permit. It provisions PostgreSQL, Valkey
(Redis-compatible), private S3-compatible object storage, an Nginx media cache,
Caddy HTTPS reverse proxy, the existing Go capability gateway, and Prometheus.
Persistent data lives in local Docker volumes.

## Private hosting control plane

The stack now includes an internal-only control-plane:8787 API for node registration/heartbeats, resource-aware workload scheduling, domain reservations, DNS verification, deployment status, usage, backup manifests and recovery-drill records. Its port is not published. It shares only the private control-plane network with Caddy and the data network with PostgreSQL; Caddy is not attached to the database network.

The fresh-database init sequence is 000_vps.sql, 001_platform.sql, 002_production_hardening.sql, then 003_hosting_control_plane.sql. PostgreSQL only runs init scripts on an empty data directory. For existing data, take and verify a backup, review/apply these migrations in order manually, and verify schema state before deploying the control-plane container.

VPS_INTERNAL_TOKEN authenticates control-plane operations, and VPS_NODE_BOOTSTRAP_TOKEN permits node registration. Generate distinct long random values and never expose either to browser/mobile clients. TESTAGRAM_BASE_DOMAIN, TESTAGRAM_PUBLIC_ORIGIN_IP, and TESTAGRAM_DOMAIN_TARGETS are intentionally blank by default. Domain reservation/activation stays unavailable until operators explicitly configure a domain they control, verify DNS and allowlist private service origins. Caddy's on-demand TLS callback rechecks DNS against the configured origin IP before authorizing a certificate. This PR does not register a public domain or write to authoritative DNS.

The current control plane can schedule work only when a healthy node has registered and reported capacity. The node agent is not automatically installed or enrolled by this Compose file; node-agent hardening, enrollment, deployment integration tests and restore drills remain release gates.

## Current boundaries — read before production use

- Valkey is used by the private platform service for cache, queues and rate limiting. The self-hosted configuration uses `--maxmemory-policy noeviction`: an `allkeys-*` eviction policy could evict queue-stream keys under memory pressure and silently discard unprocessed work. With `noeviction`, writes fail rather than evicting queue data when memory is exhausted; cache/rate-limit operations can therefore fail until capacity is restored. Alert on memory headroom and queue age, and size/scale Valkey before sustained production load. The public capability gateway and product application call sites do not yet consume those APIs, so product-level cache/queue/rate-limit behavior is not automatically active.
- MinIO provisions separate private and public media buckets. The API can issue
  authenticated, owner-prefixed S3 POST upload policies and five-minute private
  download URLs when the restricted `MINIO_MEDIA_ACCESS_KEY` /
  `MINIO_MEDIA_SECRET_KEY` identity is configured. It never returns the secret
  signing key. Public uploads are limited to approved image/video MIME types;
  private URLs are only issued for keys under the authenticated user's prefix.
  The media edge permits signed POST uploads, bypasses shared caching for signed
  private GETs, and adds CORS headers for the Testagram web origins.
  The API now persists upload intents, verifies uploaded objects with signed
  HEAD requests, records verified metadata, and reserves a configurable per-user
  quota. The API now also supports owner-scoped deletion with a durable
  `deletion_pending` retry state and a five-minute janitor that cleans up expired
  unfinished uploads and retries failed deletions. Public cache TTL is capped at
  60 seconds and stale-on-origin-error serving is disabled; this bounds stale
  public copies but is not an instantaneous purge API. Product clients still need
  wiring, and moderator authorization/workflows, transformations, thumbnails,
  and transcoding remain gates.
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
  without an authenticated admin/VPN route. The stack now scrapes a read-only
  node-exporter for host filesystem and memory metrics and loads
  `prometheus-alerts.yml` rules for low/critical disk space, memory headroom,
  missing API/platform/node metrics, queue backlog, and oldest pending-message
  age. Queue alerts only cover queue/group pairs configured in
  `PLATFORM_MONITORED_QUEUE_GROUPS`. These rules are evaluated by Prometheus;
  notifications are not delivered until an operator configures an Alertmanager
  receiver and an appropriate private alert route. A dedicated CI workflow
  validates the Prometheus config and rule syntax. The exporter requires Linux
  host mounts and is intended for the self-hosted Linux deployment.

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

## MinIO media upload signing

Set `MINIO_MEDIA_ACCESS_KEY` and a unique, long `MINIO_MEDIA_SECRET_KEY` in
`.env`. Do not reuse `MINIO_ROOT_PASSWORD`. The one-shot `object-init` service
creates a restricted MinIO identity with object read/write/delete permissions
limited to the configured private and public media buckets. `MINIO_PUBLIC_ENDPOINT`
must match the public media hostname routed through Caddy/Nginx (default:
`https://media.testagram.site`). If you change the media domain or bucket names,
update the endpoint/configuration and recreate the restricted MinIO policy as
part of a controlled maintenance window.

The API exposes:

- `POST /api/media/upload-intent`: requires a valid Supabase bearer session and
  returns a short-lived S3-compatible POST policy. Send the returned form fields
  and file to the returned URL using `multipart/form-data`; do not send the
  file to the JSON API endpoint.
- `POST /api/media/private-download-url`: requires a valid Supabase bearer
  session and returns a five-minute signed URL only when the object key belongs
  to that user.
- `POST /api/media/delete`: requires the authenticated owner and an existing
  ready asset. It records a durable deletion state before requesting a signed
  MinIO DELETE, then removes linked `post_media` rows and marks the product
  `media_assets` record deleted. A 202 response means storage/product cleanup
  will be retried by the janitor.
- `POST /api/media/attach`: requires an authenticated owner, a ready public
  media asset, and a post visible as owned by that user under PostgREST/RLS.
  The backend synchronizes a product `media_assets` UUID and an idempotent
  `post_media` join. Configure `SUPABASE_SERVICE_ROLE_KEY` only in the backend
  environment; it is never returned to the client. This bridge uses the product
  database exposed by `SUPABASE_URL` and fails closed when not configured.
- `POST /internal/media/quarantine`: operator takedown protected by
  `X-Testagram-Moderation-Token` matching the server-only
  `MEDIA_MODERATION_TOKEN`, plus a required moderator ID and reason. It writes
  an audit event and removes the object from storage; a 202 response means
  takedown is queued for retry. Generate a long random token, keep it out of
  browser/mobile apps, and restrict this route at the edge/VPN where possible.
  This is an operator primitive, not a full moderator dashboard or role system.
- `POST /api/media/complete`: after the client uploads the returned form to
  object storage, the API verifies the object with a signed HEAD request and
  persists server-reported size, MIME type and ETag in `testagram_media.assets`.
  It is idempotent for matching metadata and rejects cross-owner key conflicts.

For an existing PostgreSQL volume, back up first and apply
`migrations/005_media_assets.sql`, `migrations/006_media_lifecycle.sql`, and `migrations/007_media_product_bridge.sql` manually; Compose init scripts run only for a
new data directory. Upload, completion, cleanup, deletion, quarantine and post linking require all three migrations.

Only JPEG, PNG, WebP, AVIF, GIF, MP4 and WebM are accepted by upload intent. The API reserves upload-intent size against a per-account quota (default 2 GiB, configurable with `MEDIA_USER_QUOTA_BYTES`) inside a transaction serialized per owner, so concurrent requests cannot bypass the quota.
The policy enforces an upper bound derived from the requested file size and
configured image/video limits. The Nginx media cache must remain the only public
route to MinIO; never publish MinIO ports 9000/9001 directly. Signed private GET
URLs are marked `private, no-store` and bypass the shared cache. Public media
responses are cached for at most 60 seconds. The cache has no synchronous purge
endpoint in this release, so deletion/takedown propagation is bounded by cache
and downstream browser behavior rather than instantaneous.

## Safe node maintenance

Authenticated operator-only endpoints `POST /v1/hosting/nodes/{id}/drain` and `POST /v1/hosting/nodes/{id}/resume` support maintenance without exposing a public admin port. Drain excludes a node from new scheduling while allowing already-running work to finish; it does not kill workloads. Resume is allowed only when the node is still heartbeating and was drained. Both transitions and their metadata are committed with an audit event in the same database transaction. The application records the shared internal bearer identity, not a human identity; put this API behind a trusted operator gateway/VPN and do not share the token among people if per-user attribution is required.

The new `004_hosting_operations.sql` migration creates the private audit-event table. For existing databases, back up first and apply migrations 003 and 004 after verifying prior schema state. Fresh volumes pick up migration 004 automatically.

## Backup and recovery

Named Docker volumes are not backups. The operator-run
[`BACKUP-RESTORE.md`](BACKUP-RESTORE.md) runbook and
[`scripts/self-hosted-backup.sh`](../../scripts/self-hosted-backup.sh) now
provide a restic-based workflow that snapshots PostgreSQL plus both configured
MinIO media buckets, validates the dump, checks repository integrity, and
supports daily/weekly/monthly retention. Configure an encrypted **off-host**
restic repository and schedule it using a protected systemd timer or cron
environment; the repository and password are not provisioned by Compose.

The script stages a temporary plaintext copy while it runs. Use encrypted host
storage, a protected staging directory, and enough free disk space. It does not
back up host secrets, TLS keys, DNS configuration, or Valkey queue state, and
it does not itself configure alerts or prove a restore. Complete an isolated
restore drill and record RPO/RTO before production use. Keep at least one copy
off the primary host: a single machine cannot survive its own disk failure,
theft, fire, or power loss.

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
