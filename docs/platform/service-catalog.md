# Testagram Platform Service Catalogue

Purpose: make service ownership, API contracts, health checks, data durability, and provider substitution explicit.

This catalogue separates the Testagram-owned service boundary from implementation details. Self-hosted means operated on Testagram-controlled compute; it does not eliminate power, bandwidth, domain, hardware replacement, or off-site backup costs.

## Layer 2 — platform services

| Capability | Service / implementation | API boundary | Health / readiness | Durable state | Status |
|---|---|---|---|---|---|
| Identity/authentication | Upstream-compatible auth service in self-hosted platform distribution | /auth/v1/* | /auth/v1/health through gateway; verify against deployed release | PostgreSQL, signing keys backed up separately | Compatibility wrapper; upstream internals not replaced |
| Data API | REST gateway over PostgreSQL | /rest/v1/* | gateway probe plus database probe | PostgreSQL | Upstream-compatible |
| Realtime | Realtime service | /realtime/v1/* | release-specific endpoint smoke test | PostgreSQL is system of record; pub/sub is not | Upstream-compatible |
| Object/media API | Storage API plus Testagram media service | /storage/v1/*, /v1/videos*, /v1/channels*, /v1/feed | storage status; media /healthz and /readyz | Originals and ownership metadata | Mixed upstream/Testagram |
| Cache/rate limiting | Testagram platform API backed by Valkey/Redis protocol | /v1/cache/*, /v1/rate-limit/check | /healthz, /readyz, /metrics on private port 8095 | Disposable cache; never canonical user state | Implemented in services/platform |
| Queues | Testagram platform API backed by Valkey Streams | /v1/queue/* | /readyz checks database and Valkey | Critical jobs must be reconciled from durable DB state | API implemented; operational semantics need service tests |
| Feature configuration | Testagram platform API backed by PostgreSQL | /v1/config/set, public /v1/config/{key} | /readyz | Versioned PostgreSQL rows | Implemented |
| Events/audit | Testagram platform API backed by PostgreSQL | /v1/events, /v1/audit | /readyz | PostgreSQL rows; retention policy required | Implemented |
| Video processing | Testagram video API and FFmpeg worker | /v1/videos*, /v1/channels*, /v1/feed | API /healthz and /readyz; worker readiness endpoint | Original files and job state durable; HLS/posters rebuildable | Implemented; load/resource testing required |
| Notifications | Testagram notification API | /v1/devices, /v1/notifications, /v1/stream | /healthz, /readyz | Preferences and delivery state durable; streams can be transient | Implemented; delivery channels are adapters |
| Maintenance jobs | Testagram worker | Internal maintenance loop | /healthz, /readyz | PostgreSQL remains source of truth | Implemented |
| Distributed compute | Testagram control plane and node agent | Authenticated control-plane API; outbound node channel | control plane /healthz and /readyz | Node/workload state in PostgreSQL; scratch is disposable | Baseline implemented; keep control endpoints private |
| Hosting control plane | Testagram control-plane hosting API | /v1/hosting/domains, /v1/hosting/deployments, /v1/hosting/nodes, /v1/hosting/usage | /v1/hosting/health plus /healthz and /readyz | PostgreSQL hosting schema; workload state in PostgreSQL | API implemented in progress; DNS records are operator-applied, activation verifies origin IP, Caddy on-demand TLS is gated by active reservations; secret resolver is not implemented |
| Search | PostgreSQL search first; optional search engine behind adapter | App-owned search interface | Adapter-specific readiness probe | Index is rebuildable; canonical content in PostgreSQL | Architecture target; no separate search engine claimed |
| Email | Durable transactional outbox plus SMTP/provider adapter | Internal enqueue/send contract to finalize | Queue/worker readiness and adapter probe | Outbox and retry state durable | Adapter boundary planned |
| AI | Local inference adapter plus optional external provider adapters | Versioned AI gateway contract to finalize | Model availability, timeout, queue depth | Outputs/embeddings follow retention policy | Architecture target; no local GPU service claimed |
| Deployment/monitoring | Compose/systemd, Caddy, Prometheus-compatible metrics | Deployment and scrape interfaces | Container checks, HTTP probes, metrics | Secured config, rotated logs, defined metrics retention | Partial and host-specific |

## Layer 3 — infrastructure

| Infrastructure | Contract | Durable data | Required controls |
|---|---|---|---|
| Compute | Docker Compose; optional authenticated node agents | Workload definitions and node records in PostgreSQL | CPU/memory/PID limits, least privilege, pinned images, patching |
| Networking/TLS | Caddy ingress and private Docker networks | Persistent certificate state | Only intended public ports; renewal monitoring |
| PostgreSQL | PostgreSQL wire protocol on private network | Primary relational data | Authenticated access, migration checks, WAL-aware backup, restore drills |
| Object storage | S3-compatible adapter or local filesystem adapter | Original media and object metadata | Private by default, signed access, checksums, quotas, restore tests |
| Valkey | Redis protocol adapter | Cache disposable; queue durability explicitly configured | ACL/password, private network, memory/persistence policies, metrics |
| CDN/cache | Caddy/Nginx edge and local media cache | Cache disposable | Never cache authenticated API responses; invalidation; immutable public assets only |
| Backups | Database, object data, configuration and key escrow | Off-host copies required for disaster recovery | Encryption, access control, retention, checksums, regular restore tests |
| Domains/DNS | Authoritative DNS provider or self-hosted DNS | Zone export and registrar recovery data | Domain registration remains a cost; self-hosting DNS does not make registration free |

## Common API and health conventions

- /healthz is liveness: process is alive and should not fail solely because a dependency is down.
- /readyz is readiness: required dependencies must respond within bounded timeouts.
- /metrics is private-network-only unless protected separately at the edge.
- JSON writes use bounded request bodies, stable status codes, and strict field validation where practical.
- Internal APIs require bearer credentials or stronger workload identity. Never ship internal tokens to browsers or mobile apps.
- Cache is never authoritative for users, credentials, posts, payments, job completion, or media ownership.
- Durable jobs need idempotency, leases, bounded retries, dead-letter/reconciliation paths, and queue-age metrics.
- Adapters need contract tests so implementations can change without rewriting business logic. The reusable Go client in internal/platformclient exposes Cache, RateLimiter, and Queue interfaces over the versioned internal HTTP API; consumers should depend on these interfaces rather than importing a Redis/Valkey SDK. New consumers still need to be migrated and tested before claiming full decoupling.

## Hosting control plane implementation boundary

The private operator API is documented in [hosting-control-plane.yaml](openapi/hosting-control-plane.yaml). It reserves a subdomain beneath `TESTAGRAM_BASE_DOMAIN` only when `targetService` maps to an explicit `TESTAGRAM_DOMAIN_TARGETS` allowlist. DNS output is desired state only: it does not register a domain or modify authoritative DNS. After the operator applies the record and activation verifies it resolves to `TESTAGRAM_PUBLIC_ORIGIN_IP`, Caddy's catch-all tenant site proxies only active hostnames to allowlisted private origins and requests per-host HTTPS certificates through an on-demand authorization callback. No registrar API or automatic DNS write is included. Check the target service is reachable before activation; first-access TLS issuance still depends on public DNS reachability and ACME validation. Deployment scheduling pins an immutable image digest and reserves only capacity on a fresh node, counting active workload CPU/RAM reservations. Secret references are deliberately rejected until a real secret resolver exists; public environment keys that look secret-bearing are rejected.

The backup endpoints store manifests and verification/drill reports. A matching hash report is not by itself proof of disaster recovery; a trusted worker must read the backup and restore it to an isolated target before submitting the report. The CI contract tests do not yet substitute for a production backup restore drill.

## Migration and production cutover gate

Before moving production traffic, prove: (1) consistent database backup and isolated restore; (2) row counts/checksums and auth identity mapping; (3) RLS/policy parity including negative tests; (4) media object/byte counts, hashes, access behavior and signed URL expiry; (5) job idempotency/retries/restart recovery; (6) end-to-end login, refresh, logout, email-code, upload, read, delete and federation flows; (7) rollback steps and cutover window.

Do not delete or rename old volumes, buckets, databases, keys, or object directories during rebranding.