# InstaBox machine-pause recovery runbook

**Scope:** Testagram's InstaBox Free machine, observed in the `machine-paused` state.
**Purpose:** Recover safely after provider idle pause and use InstaBox's native wake-on-request behavior for real user traffic.

## 1. Native wake-on-request: preferred path

InstaBox now supports wake-on-use for a machine that paused itself due to inactivity: an inbound request to its published application endpoint can wake it automatically. A machine manually paused/stopped by an operator is a different lifecycle state and may require an explicit resume/start operation.

- Keep Testagram's actual public app endpoints routed to the machine's supported published port(s).
- The current self-hosted Compose stack exposes Caddy on ports 80, 443 and 8443; verify the InstaBox public-port publication maps the intended HTTPS ingress to the Caddy listener. Do not expose PostgreSQL, MinIO, Prometheus, Grafana or internal service ports.
- Users' real requests (app open, login, feed read, post creation, messaging) should arrive through the normal Cloudflare -> published HTTPS origin path. The request itself is the wake trigger; do not generate artificial keep-alive traffic.
- The first request may experience a cold-start delay or fail while the VM and containers resume. The client should show a bounded "Waking Testagram…" state and retry safe reads with exponential backoff and jitter.
- Never blindly replay non-idempotent actions such as creating a post, sending a message, wallet transfers, M-Pesa top-ups, or purchases. These must carry stable idempotency keys and the Backend must return the prior result for duplicate submissions.
- Do not create a second relay or external resume service unless native wake-on-request is unavailable for the actual published endpoint and the provider confirms an API-based wake integration is supported.

Official references:
- https://instabox.sh/blog/wake-on-use
- https://instabox.sh/docs/idle-suspend
- https://instabox.sh/docs/api/ports

## 2. Confirm the machine state and endpoint path

From an authenticated operator workstation:

```sh
instabox usage
instabox --help
```

Confirm the machine named `testagramdb` is idle-paused rather than manually paused, stopped, failed, or suspended by the provider. Check the provider status page if the machine or published endpoint remains unavailable:

- https://status.instabox.sh

Verify the published port configuration in the InstaBox dashboard/CLI. Confirm public HTTPS traffic reaches Caddy and only Caddy. Never expose internal data-plane ports.

## 3. Public-port exposure: verified Testagram mapping

The dashboard configuration supplied for `testagramdb` lists public HTTPS ports 8080, 8081, 8443 and 9000. Cross-checking the repository's actual `deploy/self-hosted/docker-compose.yml` and `Caddyfile` establishes these roles:

| Published machine port | Compose listener | Assessment |
|---|---|---|
| 8080 | Backend API host bind `127.0.0.1:8080:8080` | Direct public API path; bypasses Caddy's edge policy |
| 8081 | CDN host bind `127.0.0.1:8081:8081` | Direct public CDN path; bypasses Caddy's edge policy |
| 8443 | Caddy `:8443` listener | Intended public edge ingress |
| 9000 | MinIO object-storage host bind `127.0.0.1:9000:9000` | **Critical: direct object-storage API exposure; not the CDN** |

**Required operator action in InstaBox dashboard:** unpublish port 9000 immediately after confirming no separately approved external S3 client depends on this endpoint. Then verify Cloudflare and all Testagram hostnames route through the 8443 edge listener before unpublishing 8080 and 8081. Keep the internal Docker services and their loopback bindings; do not remove or recreate containers as part of this change. Never publish 9000/9001, PostgreSQL 5432, MinIO console 9001, Prometheus 9090, Grafana 3000, or internal service ports.

Do not unpublish 8080/8081 until the 8443 + Cloudflare path has passed the acceptance checks below. After unpublishing, verify that the four old direct URLs no longer serve application responses and that the canonical API, media and web hostnames still work through the edge. If an S3 integration legitimately needs remote access, use a dedicated authenticated private path or approved storage-provider endpoint, not the public MinIO admin/data port.

This is an operator-side dashboard change; this PR does not and cannot alter InstaBox published-port settings.

## 4. Verify persistent storage before manual recovery

If native wake-on-request does not restore the machine, connect after a supported resume/start operation and verify the existing PostgreSQL volume before starting or recreating containers.

```sh
docker compose --env-file deploy/self-hosted/.env -f deploy/self-hosted/docker-compose.yml ps
docker volume ls
```

Run from the repository root, adjusting only for the deployment's established checkout path and Compose project. Do not create a new empty volume.

**Stop and escalate** if the PostgreSQL data volume is missing, unexpectedly empty, detached, or replaced. Never run `docker compose down -v`, remove volumes, reinitialize PostgreSQL, or restore a snapshot over the live database as routine recovery.

## 4. Ordered service recovery

Inspect the existing deployment first:

```sh
docker compose --env-file deploy/self-hosted/.env -f deploy/self-hosted/docker-compose.yml config --quiet
docker compose --env-file deploy/self-hosted/.env -f deploy/self-hosted/docker-compose.yml ps
```

If containers are stopped but the persistent volume is verified, start the existing stack:

```sh
docker compose --env-file deploy/self-hosted/.env -f deploy/self-hosted/docker-compose.yml up -d
```

Do not use `--renew-anon-volumes`, `-V`, `down -v`, or destructive database initialization flags.

Inspect database recovery and readiness:

```sh
docker compose --env-file deploy/self-hosted/.env -f deploy/self-hosted/docker-compose.yml logs --since=15m postgres
docker compose --env-file deploy/self-hosted/.env -f deploy/self-hosted/docker-compose.yml exec -T postgres pg_isready
docker compose --env-file deploy/self-hosted/.env -f deploy/self-hosted/docker-compose.yml ps
```

Probe from outside the machine:

```sh
curl --fail --show-error --silent https://api.testagram.site/healthz
curl --fail --show-error --silent https://api.testagram.site/readyz
```

A running process is not enough: readiness and public-origin probes must pass.

## 5. Post-resume integrity and work reconciliation

Before declaring recovery complete:

1. Inspect PostgreSQL logs for crash recovery, WAL replay errors, corruption, read-only state, or failed checkpoints.
2. Verify the latest successful encrypted off-machine backup and its integrity record. An InstaBox snapshot alone is not an independent backup.
3. Inspect durable outboxes/workers for expired leases and stuck in-progress work using each service's reviewed operator queries.
4. Reconcile payment-provider callbacks and wallet ledger state before retrying M-Pesa or wallet actions. Never manually credit a wallet to compensate for a timeout.
5. Verify Federation, Data Centre workers, media replication, CDN origin delivery, and web/API end-to-end smoke tests.
6. Record incident start/end, provider state, recovery actions, data checks, and any jobs requiring reconciliation.

Do not run unreviewed SQL updates against ledger, outbox, or migration tables during recovery.

## 6. Escalation conditions

Stop automated recovery and escalate if:

- the machine is manually stopped/paused or in a failed state rather than idle-paused;
- the published port does not wake the idle-paused machine;
- the expected PostgreSQL volume is absent or has an unexpected identity;
- PostgreSQL cannot complete crash recovery or reports corruption;
- migrations fail unexpectedly, backups are missing/stale, or duplicate financial/outbox effects are suspected;
- the machine repeatedly pauses/stops or the provider reports an incident.

## 7. Production eligibility

Wake-on-request improves cold-start behavior; it does not provide continuous availability or an uptime SLA. Free-plan runtime and maximum-run limits still apply. Production must move to a paid InstaBox tier with idle auto-suspend disabled or another explicitly approved always-on host, with external monitoring, encrypted off-machine backups, restore drills, and measured RPO/RTO.

Do not use scheduled pings, busy loops, activity leases, or pause/resume cycling to evade Free-plan limits. A paid InstaBox machine is still a hosted microVM, not proof of dedicated physical hardware or high availability.

## Related tracking

Implementation and acceptance evidence: [Backend issue #442](https://github.com/Trendyzima/Testagram-backend-/issues/442).
