# Testagram scalable architecture

## System boundaries

1. Clients call public APIs over HTTPS.
2. CDN/data plane (cdn-) serves images, video, HLS/DASH segments and cacheable public media. It must not proxy every media byte through application handlers.
3. Edge/load-balancing layer routes requests to healthy API instances and enforces TLS, request-size limits and baseline abuse protection.
4. Stateless application API validates requests and performs short database operations. Any instance should be replaceable without losing user session state.
5. Supabase/PostgreSQL remains the source of truth for accounts, posts, relationships and metadata. Auth remains owned by Supabase Auth unless a reviewed migration explicitly changes it.
6. Queues and workers process federation discovery/delivery, feed fanout, notifications, media metadata and other slow tasks outside request handlers.
7. Object storage holds binary media. PostgreSQL stores durable metadata and references, not video blobs.
8. Observability and recovery include structured logs, metrics, traces, backups and restore drills.

## Non-negotiable reliability patterns

- Every outbound request and database operation has a deadline.
- Retries are bounded, use exponential backoff with jitter, and are used only for safe/idempotent operations.
- Jobs have idempotency keys, bounded concurrency, dead-letter handling and visibility into queue age.
- Apply per-user/per-IP rate limits at the edge and per-operation limits in the API.
- Use cursor/keyset pagination for large timelines; do not use unbounded offset scans for feed traversal.
- Add indexes from measured query plans; avoid speculative indexes and schema rewrites.
- Use a bounded database connection pool per API instance. Sum of all pools must stay below the database connection budget with reserve capacity.
- Keep health checks distinct: liveness tests the process, readiness checks critical dependencies.
- Prefer cache-aside with explicit TTL/invalidation rules. Never treat cache as the source of truth for permissions or private content.
- Protect public and private media differently. Signed URLs/tokens must expire and be validated by the CDN/origin path.
- Use schema migrations with backwards-compatible expand/contract deployments.
- Fail closed on authorization errors and do not log credentials, JWTs, signed media URLs or personal content.

## Scale-out sequence

### Phase 0 — Baseline and correctness
Audit existing routes, Supabase RLS policies, auth flows, feed/search queries, federation tasks and CDN origin paths. Record p50/p95/p99 latency, error rates, DB query time, pool wait time and background queue age.

### Phase 1 — One reliable deployment
Deploy the API behind TLS, use managed or self-hosted Supabase with private database connectivity, external object storage/CDN, automated backups and alerting. Keep local development isolated.

### Phase 2 — Horizontal API and worker scaling
Make API instances stateless; deploy multiple replicas behind a health-aware load balancer. Scale workers independently by queue age and processing throughput. Enforce a total connection budget across all replicas.

### Phase 3 — Database and cache optimization
Use EXPLAIN (ANALYZE, BUFFERS) on expensive queries with safe representative data. Add only justified indexes. Introduce pooling, read replicas for eligible read traffic, partitioning or specialized search only when metrics show a need and consistency tradeoffs are understood.

### Phase 4 — Multi-region resilience
Define data residency and consistency requirements before adding regions. Keep one authoritative write path unless a deliberate multi-writer design is implemented. Exercise failover, backup restore, key rotation and regional loss.

## Capacity truth

Five million registered accounts are not equivalent to five million concurrent users. Capacity must be specified as requests/second, active WebSocket connections, read/write mix, payload size, media egress, and latency/error SLOs. A successful in-process fan-out test is not proof of Internet-scale capacity. Never publish a capacity claim without a reproducible test against deployed infrastructure.
