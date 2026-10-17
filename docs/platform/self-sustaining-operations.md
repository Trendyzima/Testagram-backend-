# Testagram Self-Sustaining Operations Blueprint

This document is the operational target for a Testagram-owned platform. It distinguishes what exists today from capabilities that still need implementation and evidence. A self-hosted service is not considered complete until it has a deploy path, API contract, health/readiness probe, bounded resource use, backup/recovery behavior, metrics, and a tested rollback.

## 1. Ownership model

Testagram owns the control plane, application contracts, deployment definitions, secrets policy, service discovery conventions, observability, backup policy, and provider adapters. Mature open-source components may implement the underlying work. Replacing every upstream component with a home-grown clone would increase security and maintenance risk without making the platform more self-sufficient.

Use three layers:

- **Layer 1 — Testagram applications:** web/mobile, feed, federation, auth UX, media, notifications and business logic.
- **Layer 2 — Testagram platform services:** identity boundary, API gateway, cache/rate limits, durable queues, object/media API, search, email, AI gateway, deployment controller, service registry, metrics and audit.
- **Layer 3 — infrastructure:** compute nodes, private networks, PostgreSQL, object disks, backups, TLS ingress and DNS.

Each Layer 2 capability must have a versioned interface and provider adapter. Application code must not need to know whether cache is Valkey or Redis, object storage is filesystem or S3-compatible, email is local SMTP or a relay, or compute runs on the primary host or a registered worker node.

## 2. Domains and DNS without unnecessary subscriptions

### What can be self-hosted

- Internal service discovery can use Docker's private service DNS (for example `platform`, `redis`, `video`) on private networks. These names must never be used as public URLs.
- A Testagram-operated private zone such as `svc.testagram.internal` can be served by CoreDNS on the private network when clients/nodes are configured to use it. Keep zone data and configuration in version control, with secrets excluded.
- Caddy can terminate TLS, route public hostnames to private services, renew certificates, and cache explicitly public immutable assets.
- DNS zone exports, registrar recovery instructions, TLS expiry checks, and domain-to-service mapping should be stored and monitored by Testagram.

### What cannot honestly be made free by self-hosting

A globally reachable name such as `testagram.site` needs a registered domain (or an equivalent name allocated by a third party), and public DNS delegation must be reachable from the Internet. Running an authoritative DNS server does not eliminate the registrar/registry cost and does not automatically protect against power loss, ISP filtering, DDoS, or a changing residential IP.

Keep the current public domain and DNS routing unchanged while testing. Before any future cutover, lower TTL in advance, export the zone, verify TLS and origin reachability, document rollback records, and test the rollback path. Never point public DNS at an unverified machine.

## 3. Hosting and deployment

### Current foundation

- `vps/docker-compose.yml` composes the Testagram control plane, platform API, media services, worker, Valkey and Caddy.
- `backend/docker-compose.yml` defines reusable service implementations.
- `testagram-platform/` wraps the upstream-compatible auth, REST, realtime and object storage runtime.
- The platform client and OpenAPI contract define an internal service boundary.

These are building blocks, not proof that a zero-touch deployment, disaster recovery, multi-node scheduling, or all application integrations are finished.

### Required deployment pipeline

1. Run unit tests, race tests, static analysis, OpenAPI validation, Compose rendering, container builds, migration checks, and API contract tests.
2. Publish immutable images identified by commit SHA; do not deploy a floating `latest` tag.
3. Validate configuration and secrets without printing them.
4. Back up the database and object metadata before schema-changing releases; verify backup age and destination.
5. Deploy to an isolated staging project and run synthetic sign-in, read/write, media upload/playback, queue recovery, and federation tests.
6. Roll out one service group at a time. Use health/readiness checks and a bounded observation window.
7. Stop rollout on a failed probe or elevated error rate. Roll back application images automatically only when database compatibility is known; otherwise require a migration-specific recovery plan.
8. Record commit, image digests, schema version, health results and operator action in the audit log.

Do not use `docker compose down -v`, delete persistent volumes, or rename data directories as part of routine deployment. Keep a tested restore path independent of the primary host.

## 4. Compute and resource scheduling

Use one host as the initial control-plane/worker node, then add nodes only when measured demand requires them. A registered worker must authenticate, report capacity and health, receive only explicitly assigned workloads, and be revocable without losing durable application data.

- Put CPU, memory, PID and disk-I/O bounds on each service. Keep video transcoding and local AI inference in separately limited worker pools so they cannot starve auth or the feed.
- Give latency-sensitive APIs reserved capacity; use bounded queues and backpressure for heavy jobs.
- Use temporary disk for scratch/transcode fragments; keep originals, PostgreSQL data, job state, signing keys and object metadata on durable storage.
- Jobs must have stable IDs, idempotency keys, leases, bounded retries, dead-letter handling and reconciliation from PostgreSQL after a worker crash.
- Advertise worker capacity honestly. A node that is offline, full, stale, or fails attestation must not receive new jobs.
- Start with simple placement rules (health, available RAM/disk, workload class). Do not introduce a complex cluster scheduler until metrics justify it.

## 5. Performance and footprint

Optimize for the user's latency and reliability, not for the smallest container image alone.

- Keep internal service calls on private Docker networks; avoid public Internet hairpins between services on the same host.
- Use PostgreSQL as the system of record, connection pooling where justified, query plans, appropriate indexes, bounded page sizes and keyset pagination for deep feeds.
- Use Valkey only for disposable cache, rate-limit counters, coordination, and explicitly configured queue persistence. Cache misses must degrade safely; no authoritative user data may exist only in cache.
- Use asynchronous processing for video, thumbnails, email and expensive AI. Cap upload sizes, concurrent transcodes, output duration, retries and request timeouts.
- Serve public immutable assets through Caddy/cache with correct cache headers. Do not cache authenticated API responses or private media.
- Apply lazy loading and bounded pagination in clients; do not preload an infinite feed into memory.
- Measure p50/p95/p99 latency, error rate, queue age, database pool saturation, cache hit ratio, CPU throttling, RSS, disk free space and backup age before tuning.

## 6. Durability and recovery

| Data | Source of truth | Recovery requirement |
|---|---|---|
| Users, credentials metadata, posts, permissions, job state | PostgreSQL and protected key material | Consistent backup, isolated restore, policy tests |
| Original uploads and media | Durable object/filesystem storage plus metadata | Byte/object count and checksum verification |
| Transcoded HLS, thumbnails, derived search index | Rebuildable artifacts | Rebuild from original plus durable job state |
| Cache and ephemeral presence | Valkey/memory | Safe loss and refill |
| Critical queues | Durable job record in PostgreSQL; queue is delivery mechanism | Reconcile pending jobs after restart; idempotent execution |
| Secrets, signing keys and encryption keys | Protected secret store/off-host encrypted escrow | Restricted recovery access and rotation drill |
| DNS, deployment configuration and image digests | Versioned config and encrypted backup where secrets exist | Restore instructions tested away from primary host |

Backups on the same disk are not disaster recovery. Keep encrypted off-host copies, use retention and checksums, and run regular restore drills. Define recovery-point and recovery-time objectives before claiming high availability.

## 7. Domain, hosting and service health inventory

Maintain one inventory row per service with: owner, image digest, host/node, internal address, public route if any, API version, liveness URL, readiness URL, metrics, persistent mounts, backup class, resource limits, alert thresholds, deployment owner and rollback command.

- `/healthz`: process liveness only.
- `/readyz`: dependency-aware readiness with short timeouts.
- `/metrics`: private-network or authenticated access only.
- All externally reachable routes terminate TLS and are rate limited.
- PostgreSQL, Valkey, control-plane admin routes and internal platform APIs stay private.
- Alert on certificate expiry, failed backups, restore-test age, disk pressure, repeated restarts, queue age, stale workers and readiness failures.

## 8. What “self-sustaining” means in practice

Self-sustaining means Testagram can deploy, observe, recover, rotate, and replace its service implementations without rewriting the product or relying on one managed vendor. It does **not** mean perpetual operation without electricity, hardware, bandwidth, domain registration, security updates, off-host recovery, or a responsible operator.

## 9. Release gates before claiming completion

- [ ] Full CI green on the exact commit.
- [ ] Every internal endpoint has authentication, validation, timeout, size limits and contract tests.
- [ ] Actual application callers use the platform adapters; no unused facade is counted as integration.
- [ ] Compose config renders from a clean host with documented environment variables.
- [ ] No required secret has a default value; no secret is logged or committed.
- [ ] Cold start, host reboot, database outage, cache loss, worker crash and disk-pressure tests pass.
- [ ] Backup restore works on a separate host and object checksums match.
- [ ] Auth, RLS, email-code, refresh/logout, upload, playback, delete and federation end-to-end flows pass.
- [ ] Public DNS cutover and rollback are rehearsed without changing production during the test.
- [ ] Resource limits and latency targets are measured under representative load.

Until the gates are evidenced, describe the platform as **in progress**, not 100% complete.
