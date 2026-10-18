# One-billion-user scale architecture and execution plan

## Status and promise boundary

This document is a target architecture and phased engineering plan, not a claim that the current deployment can serve one billion users today. The current self-hosted stack and virtual-storage changes must pass their existing CI, security, isolation, backup/restore, and live deployment gates before production enablement. A single VPS cannot serve as the production architecture for one billion users worldwide.

"One billion users" must be decomposed into registered accounts, monthly active users (MAU), daily active users (DAU), peak concurrent sessions, requests/second, writes/second, media ingest, media egress, and regional distribution. Capacity decisions must use measured workload profiles, not registered-user count alone.

## Target architecture

1. **Global ingress and edge**
   - Anycast/DNS traffic steering, regional health checks, DDoS protection, WAF, bot/abuse controls, TLS termination, and per-tenant/per-account rate limits.
   - CDN caching for immutable public media and public read-heavy content. Private content must use authorization-aware origin access and short-lived signed delivery; never cache private responses under a shared public cache key.
   - Explicit request budgets, body-size limits, timeouts, concurrency caps, and overload shedding at every boundary.

2. **Stateless application cells**
   - Deploy multiple independent regional cells. Each cell contains API instances, background workers, regional cache, queue consumers, and access to the appropriate data partitions.
   - Keep API instances stateless; store sessions/tokens and durable state in managed/shared services with defined failure semantics.
   - Use cell isolation so one bad deploy, tenant, region, or queue backlog does not exhaust all regions. Start with one region and a tested standby, then add regions based on measured demand.
   - Avoid synchronous cross-region calls in ordinary request paths. Define data residency, ownership, and failover rules per data domain.

3. **Data architecture**
   - PostgreSQL remains authoritative for transactional records, but a single primary database is not a billion-user scaling strategy. Partition data by a stable tenant/account key; establish ownership and migration rules before sharding.
   - Use connection pooling, short transactions, indexes validated against production-like query plans, statement/lock timeouts, read replicas for suitable read workloads, and explicit write routing.
   - Do not assume all data can be active-active. Select consistency and conflict rules per domain; identity, billing, quotas, and authorization generally require strong transactional semantics.
   - Redis is an expendable cache/coordination layer, not the source of truth for durable work, quota accounting, or payments. Cache invalidation, stampede protection, memory bounds, and fail-open/fail-closed policy must be explicit.
   - Move search and analytics to purpose-built systems fed by durable, replayable events; do not run expensive analytics against the transactional primary.

4. **Media and object storage**
   - Keep media bytes out of PostgreSQL and application local disks. Use private-by-default buckets, tenant-scoped keys, versioning/retention where needed, checksums, lifecycle rules, and tested deletion.
   - For large files, prefer provider-enforced signed upload policies with content-length constraints or a resumable multipart design with bounded part sizes. A proxy gateway is a safe fallback only when its bandwidth, temporary storage, and concurrency are budgeted and load-tested; it must not become an unbounded single-host bottleneck.
   - Enforce size while bytes are received, verify SHA-256 over actual stored bytes, bind completion to a single-use upload intent, and reject replay/races. Apply per-tenant quotas and rate limits before allocating expensive resources.
   - Serve public immutable media through CDN; serve private media through authorization-aware delivery. Track storage bytes, requests, egress, transformation costs, and abuse by tenant.

5. **Durable asynchronous processing**
   - Use a durable queue/event log with at-least-once delivery, idempotent consumers, bounded retries, dead-letter handling, replay tooling, and queue-age SLOs.
   - Separate worker pools for media transforms, feed/federation, notifications, search indexing, and scheduled tasks. Autoscale on backlog age and throughput, not CPU alone.
   - Use an outbox or equivalent transactional handoff where database writes must reliably create events. Do not promise exactly-once processing across independent systems.

6. **Multi-tenant hosted platform**
   - Every API request must derive tenant identity from authenticated server-side context; never trust a client-supplied tenant ID alone.
   - Enforce membership, project/resource ownership, quotas, concurrency budgets, billing status, and audit trails transactionally. Test cross-tenant reads/writes and identifier-guessing attacks.
   - Provision resources via durable workflows with idempotency keys, explicit state transitions, compensation, and reconciliation. Never create billable infrastructure solely from an unvalidated client request.

7. **Security and operations**
   - Secrets manager, least-privilege service identities, rotation, supply-chain scanning/signing, dependency patching, encrypted transport/storage, audit logs, abuse response, and tested incident runbooks.
   - Observe RED metrics (rate, errors, duration), saturation, queue age, DB pool waits, replication lag, cache hit rate, storage capacity, upload spool use, CDN hit ratio, egress, and per-tenant cost.
   - Define SLOs and error budgets. Alert on user-visible symptoms and leading indicators; sample high-volume traces and avoid logging secrets or private media URLs.

8. **Resilience and disaster recovery**
   - Define RPO/RTO per data domain. Keep encrypted backups outside the failure domain and test point-in-time recovery plus object-data restoration on a schedule.
   - Exercise zone loss, region loss, database failover, queue outage, cache loss, object-store degradation, bad deployment rollback, credential rotation, and restore from clean infrastructure.
   - Failover must be rehearsed and measured; DNS changes or a green health endpoint alone do not prove recovery.

## Capacity model before purchasing or deploying infrastructure

Measure at least:
- registered users, MAU/DAU, peak concurrent users, sessions per user, and geography;
- peak and sustained requests/second by endpoint, p50/p95/p99 latency, error rate, and payload size;
- database reads/writes per request, transaction duration, hot-key concentration, pool wait time, replication lag, and storage growth;
- media upload count/bytes per second, maximum object size, concurrent uploads, transform CPU/GPU, CDN hit ratio, and egress;
- queue event rate, processing time, retry rate, oldest-message age, and replay rate;
- cost per active user, request, GB stored, GB delivered, and background job.

Use production-like load tests with realistic skew, hot accounts, large tenants, cache misses, and bursts. Establish a baseline, test at 2x expected peak, test overload behavior, and test recovery after the load stops. Never extrapolate a billion-user capacity claim from a small synthetic request test.

## Phased delivery gates

### Phase 0 — Current release safety
- Keep virtual storage disabled until current PR checks are green and reviewed.
- Run Go tests, race detector where feasible, integration/security tests, migration tests, and container/stack validation on the exact proposed commit.
- Exercise upload limit enforcement for fixed-length and chunked requests, interrupted uploads, duplicate/concurrent intent use, wrong content type, expired intents, checksum mismatch, and object-store failure.
- Configure proxy body limits and timeouts, dedicated bounded upload spool, disk alerts, stale spool cleanup, and per-user/per-tenant concurrent-upload limits.
- Back up the live database off-host; verify archive integrity and restore it into an isolated environment before applying migrations 008/009.
- Verify public/private access, cross-tenant denial, deletion, expiry, cache invalidation, and restore on the actual deployment.

### Phase 1 — Instrumented single-region production
- Add dashboards and alerts, SLOs, resource limits, capacity estimates, on-call runbooks, and load-test automation.
- Run baseline, peak, burst, and soak tests; fix saturation before adding features.
- Test rolling deploys, rollback, node restart, queue replay, and database recovery.

### Phase 2 — Horizontal scaling and workload isolation
- Run multiple API instances behind a load balancer; remove local disk/session assumptions.
- Separate worker pools and queue classes. Add admission control and tenant fairness.
- Load-test database pooling, cache failure, object storage, CDN behavior, and backpressure.

### Phase 3 — Multi-zone resilience and regional expansion
- Deploy across availability zones, automate failover, and prove RPO/RTO.
- Add a second region only after data ownership, consistency, routing, residency, and disaster-recovery behavior are designed and tested.
- Use regional cells and progressive rollouts; prevent one region's failure from cascading into another.

### Phase 4 — Partitioned data and global operation
- Partition/shard only when measured bottlenecks justify the operational cost. Test rebalancing, hot-partition mitigation, online migrations, and tenant moves.
- Establish regional support/on-call, incident command, security response, vendor exit plans, and cost guardrails.
- Run continuous capacity tests and periodic region-loss and clean-restore drills.

### Phase 5 — Billion-user objective
- Treat this as a multi-year program with dedicated platform, data, security, SRE, and cost engineering.
- Validate each workload class against explicit peak and growth assumptions. Set separate SLOs for API, feeds, media, search, notifications, and control-plane provisioning.
- Do not announce one-billion-user capacity until independent load tests, regional failure drills, security reviews, and restore evidence demonstrate the stated operating envelope.

## Initial acceptance criteria

The platform is not ready to move to the next phase until:
- the exact release commit has all required CI/security checks passing;
- no tenant-isolation or authorization bypass is found in automated and manual tests;
- uploads are bounded during receipt, completion verifies actual stored bytes, and concurrent/replayed intents cannot corrupt object state;
- backup restoration and migration rollback/recovery have been demonstrated on an isolated target;
- load tests report the tested hardware, topology, traffic mix, throughput, p95/p99 latency, errors, saturation, and cost;
- operational dashboards and actionable alerts cover database, queues, object storage, temporary disk, CDN, and regional health.

## Current known constraints

The current project has a self-hosted foundation and pending virtual-storage release gates. It has not been proven to support one billion users. A single VPS, a single database writer, local upload spooling, or a single-region object store must not be represented as globally resilient at that scale. The next safe step is to close Phase 0 and gather workload measurements before choosing a scale-out topology.
