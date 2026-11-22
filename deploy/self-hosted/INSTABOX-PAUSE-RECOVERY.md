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

## 2. Existing external watchdog: Free-plan-safe policy

The repository previously contained a five-minute scheduled watchdog that probed the public API and defaulted automatic provider resume/start actions to enabled. That behavior is not appropriate for this Free-plan machine: scheduled public probes can act as artificial keepalive traffic, and scheduled pause/resume cycling can defeat the provider's idle/max-run controls.

The watchdog workflow is now operator-dispatched only. Its automatic lifecycle recovery switch defaults to `false`; a human operator must explicitly authorize a recovery run. For ordinary idle pause, rely on InstaBox native wake-on-request from genuine Testagram user traffic. Keep the separate read-only machine metadata workflow manual-only. Do not add a cron schedule or periodic public health probe to keep the Free machine awake. The Flutter repository also had 15-minute schedules for the public domain/ActivityPub smoke test and the TV health monitor; Flutter PR [#121](https://github.com/Trendyzima/testagram-flutter-client-/pull/121) removes those unattended triggers and adds CI assertions. Keep both checks available for deliberate manual dispatch.

## 3. Confirm the machine state and endpoint path

From an authenticated operator workstation:

```sh
instabox usage
instabox --help
```

Confirm the machine named `testagramdb` is idle-paused rather than manually paused, stopped, failed, or suspended by the provider. Check the provider status page if the machine or published endpoint remains unavailable:

- https://status.instabox.sh

Verify the published port configuration in the InstaBox dashboard/CLI. Confirm public HTTPS traffic reaches Caddy and only Caddy. Never expose internal data-plane ports.

## 4. Public-port exposure: verified Testagram mapping

The dashboard configuration supplied for `testagramdb` lists public HTTPS ports 8080, 8081, 8443 and 9000. Cross-checking the repository's actual `deploy/self-hosted/docker-compose.yml` and `Caddyfile` establishes these roles:

| Published machine port | Compose listener | Assessment |
|---|---|---|
| 8080 | Backend API host bind `127.0.0.1:8080:8080` | Direct public API path; bypasses Caddy's edge policy |
| 8081 | CDN host bind `127.0.0.1:8081:8081` | Direct public CDN path; bypasses Caddy's edge policy |
| 8443 | Caddy `:8443` listener | Intended public edge ingress |
| 9000 | MinIO object-storage host bind `127.0.0.1:9000:9000` | **Critical: direct object-storage API exposure; not the CDN** |

**Critical operator action:** unpublish port 9000 in the InstaBox dashboard after checking the Data Centre deployment for any explicitly approved external S3 client. The current Backend Compose configuration uses `object-storage:9000` on the private Docker network; it does not require public MinIO ingress. Do not change the internal MinIO service or loopback mapping when removing the InstaBox public port. Never publish 9000/9001, PostgreSQL 5432, MinIO console 9001, Prometheus 9090, Grafana 3000, or internal service ports. The guarded manual workflow `.github/workflows/instabox-unpublish-minio-public-port.yml` is added in PR #443: add a dedicated `INSTABOX_PORT_ADMIN_API_KEY` GitHub secret with only `machines:write` scope (do not reuse the read-only watchdog key), run first with `dry_run=true`, then—only after confirming no external S3 consumer—run with `dry_run=false` and both confirmation inputs set. It verifies the exact machine ID, port number, dashboard label, public access mode and published URL before issuing `DELETE /v1/machines/{id}/ports/{port}` with an idempotency key, then verifies the port is absent. The route and `204` response were checked against InstaBox's official OpenAPI reference. This workflow has not been run and port 9000 remains published until its verified result is recorded.

**Do not unpublish 8080 or 8081 yet.** The current Caddy `:8443` listener routes the www frontend and ActivityPub paths, then redirects other requests to www; it does not currently reverse-proxy the API or media CDN. The API and CDN public ports may therefore be the active Cloudflare origins. First run the existing read-only Cloudflare domain/Worker audit and verify the exact DNS records, Worker routes, origin hostnames, and port mapping. Only after that evidence exists should we implement a tested edge consolidation (if desired) and remove 8080/8081. Blindly unpublishing either could take the API or media delivery offline.

The public 8080/8081 origins still need a security review: confirm Cloudflare proxy/origin restrictions, TLS behavior, rate limits, trusted client-IP handling, and that no internal-only route is reachable. This PR does not change InstaBox published-port settings, Cloudflare DNS, or Worker configuration.

## 5. Live public-ingress acceptance results (2026-11-22)

A one-time read-only external check was performed against the dashboard's published URLs and canonical hostnames while the machine was running:

| Probe | Observed result | Interpretation |
|---|---|---|
| Published 8080 `/healthz` | HTTP 200, `testagram-api` | Backend process is reachable |
| Published 8080 `/api/search?q=testagram` | HTTP 200, valid Backend JSON envelope | API handler is reachable on direct origin |
| Published 8080 `/api/feed/home` | HTTP 401 | Expected unauthenticated response; route exists |
| Published 8081 `/healthz` | HTTP 200, `testagram-cdn` | CDN process is reachable |
| Published 8443 `/_testagram/edge-health` | HTTP 200, `testagram-apex-edge` | Caddy edge listener is reachable |
| `media.testagram.site/healthz` | HTTP 200, CDN reports R2 | Canonical media hostname is healthy |
| `www.testagram.site/flutter_bootstrap.js` | Asset returned | Flutter web bootstrap is reachable |
| `api.testagram.site/healthz` | HTTP 404 | **Production API hostname is misrouted** |
| `api.testagram.site/api/feed/home` | HTTP 404 | Canonical API path does not reach Backend |
| `api.testagram.site/api/search?q=testagram` | HTTP 404 | Canonical API path does not reach Backend |

This A/B result shows that the Backend itself is running, but the canonical API hostname is not reaching the same API surface. This is a production-blocking ingress defect, tracked in [Backend issue #444](https://github.com/Trendyzima/Testagram-backend-/issues/444). The next step is a read-only Cloudflare DNS/Worker audit of the API hostname, origin, port, host header and path rewrites. Do not change Flutter's canonical API base URL, point clients at the ephemeral InstaBox URL, or alter DNS/Worker configuration without an evidenced mapping and rollback plan.

The public 9000 root and MinIO health-path fetches could not be reliably interpreted by the external content probe; the Compose mapping still identifies the published port as MinIO object storage. Treat the publication as an unnecessary public object-storage surface until the operator unpublishes it and verifies that no approved external S3 client depends on it.

### Cloudflare API route root cause (read-only account audit)

The successful Cloudflare API audit (GitHub Actions run 44997378902) established the configuration behind the 404:

- `api.testagram.site` has a proxied AAAA record with placeholder target `100::`.
- The Testagram zone has no Worker route for `api.testagram.site/*`.
- A second read-only audit of Worker Custom Domains found that `api.testagram.site` is an enabled Custom Domain owned by the legacy `testagram-api` Worker. The route-only audit had not captured this attachment.
- Its `APP_ORIGIN` plain-text binding is `https://testagram.site` (the Pages frontend), not the Backend. It also has legacy Supabase secret/plain-text bindings and source references; its latest deployment is dated 2026-09-14.
- Do not delete or detach this legacy Worker during the API cutover. Cloudflare supports a Worker Route in front of a Custom Domain. PR #445 preflights that the only existing Custom Domain owner is this known legacy Worker, then attaches the clean gateway route ahead of it. The gateway fetches the published InstaBox Caddy hostname directly, so it does not recurse into the legacy Custom Domain. Keep the old Worker/domain as a rollback artifact until post-cutover acceptance.
- The `testagram.site` and `www.testagram.site` Pages domains are active. The media hostname is routed separately through R2/Worker paths.

A separate clean API gateway is proposed in [Backend PR #445](undefined). It targets the operator-provided published port 8080 URL, has no Supabase/Xclone bindings, preserves auth and idempotency, removes spoofed forwarding headers, and disables caching. It is manual-deploy only; this PR has not changed Cloudflare production routing.

## 6. Verify persistent storage before manual recovery

If native wake-on-request does not restore the machine, connect after a supported resume/start operation and verify the existing PostgreSQL volume before starting or recreating containers.

```sh
docker compose --env-file deploy/self-hosted/.env -f deploy/self-hosted/docker-compose.yml ps
docker volume ls
```

Run from the repository root, adjusting only for the deployment's established checkout path and Compose project. Do not create a new empty volume.

**Stop and escalate** if the PostgreSQL data volume is missing, unexpectedly empty, detached, or replaced. Never run `docker compose down -v`, remove volumes, reinitialize PostgreSQL, or restore a snapshot over the live database as routine recovery.

## 7. Ordered service recovery

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

## 8. Post-resume integrity and work reconciliation

Before declaring recovery complete:

1. Inspect PostgreSQL logs for crash recovery, WAL replay errors, corruption, read-only state, or failed checkpoints.
2. Verify the latest successful encrypted off-machine backup and its integrity record. An InstaBox snapshot alone is not an independent backup.
3. Inspect durable outboxes/workers for expired leases and stuck in-progress work using each service's reviewed operator queries.
4. Reconcile payment-provider callbacks and wallet ledger state before retrying M-Pesa or wallet actions. Never manually credit a wallet to compensate for a timeout.
5. Verify Federation, Data Centre workers, media replication, CDN origin delivery, and web/API end-to-end smoke tests.
6. Record incident start/end, provider state, recovery actions, data checks, and any jobs requiring reconciliation.

Do not run unreviewed SQL updates against ledger, outbox, or migration tables during recovery.

## 9. Escalation conditions

Stop automated recovery and escalate if:

- the machine is manually stopped/paused or in a failed state rather than idle-paused;
- the published port does not wake the idle-paused machine;
- the expected PostgreSQL volume is absent or has an unexpected identity;
- PostgreSQL cannot complete crash recovery or reports corruption;
- migrations fail unexpectedly, backups are missing/stale, or duplicate financial/outbox effects are suspected;
- the machine repeatedly pauses/stops or the provider reports an incident.

## 10. Production eligibility

Wake-on-request improves cold-start behavior; it does not provide continuous availability or an uptime SLA. Free-plan runtime and maximum-run limits still apply. Production must move to a paid InstaBox tier with idle auto-suspend disabled or another explicitly approved always-on host, with external monitoring, encrypted off-machine backups, restore drills, and measured RPO/RTO.

Do not use scheduled pings, busy loops, activity leases, or pause/resume cycling to evade Free-plan limits. A paid InstaBox machine is still a hosted microVM, not proof of dedicated physical hardware or high availability.

## Related tracking

Implementation and acceptance evidence: [Backend issue #442](https://github.com/Trendyzima/Testagram-backend-/issues/442).


**Verified edge-ingress correction (2026-11-22):** A one-time external GET to `https://8443-agq4me2xgrgx5ge52ts437tqea.instabox.tech/_testagram/edge-health` returned `{"ok":true,"service":"testagram-apex-edge"}`. This proves the published 8443 Caddy listener is reachable. The canonical `https://api.testagram.site/healthz` still returns 404 until the dedicated Worker route is deployed. The Worker must target **published port 8443**, not direct Backend port 8080, because Caddy enforces `EDGE_PROXY_SHARED_TOKEN` and routes only trusted API Worker requests to the private API. Before enabling the Worker, read the existing `EDGE_PROXY_SHARED_TOKEN` from the host `.env` without printing it and store that exact value as GitHub Actions secret `TESTAGRAM_EDGE_PROXY_SHARED_TOKEN` if it is already 64 hexadecimal characters. If the host value is missing or malformed, generate one independent 32-byte random token (for example, `openssl rand -hex 32`), update the host `.env` securely, then recreate/restart only Caddy with `docker compose --env-file deploy/self-hosted/.env -f deploy/self-hosted/docker-compose.yml up -d --no-deps --force-recreate caddy` from the Backend repository root so the new environment is loaded (schedule the brief edge restart); recheck `/_testagram/edge-health` before deploying the Worker. Do not paste the token into issues, PR comments, source files, logs, or chat. The deploy workflow also requires a separate least-privilege `CLOUDFLARE_WORKERS_DEPLOY_TOKEN`: Account → Workers Scripts:Edit scoped to account `77dbe7a859196105dc84fbb6b7094019`, plus Zone → Workers Routes:Edit, Zone:Read and DNS:Read scoped to `testagram.site`. The workflow preflights proxied DNS, Worker Custom Domains and existing Worker routes before changing anything. The read-only Cloudflare audit token must not be reused for deployment. Keep 8080/8081 published only until their separate origin paths are migrated and verified.
