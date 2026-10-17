# Capacity model: plan for 500 million registered accounts

## The number is not a capacity test

"500 million users" must be translated into active users, concurrent requests, read/write mix, open streaming connections, payload sizes, and media egress. A registration count by itself does not determine server capacity.

Illustrative arithmetic only (not forecasts or measured results):
- 500M registered × 10% daily active = 50M daily active accounts.
- 10% of that daily active population concurrently online = 5M concurrent users.
- One API request per concurrent user every 30 seconds averages about 167,000 requests/second; every 10 seconds averages about 500,000 requests/second.
- 5M simultaneous video viewers averaging 1 Mbps each require 5 Tbps of media egress. This must be delivered by distributed CDN edges and origin shields, not the application API.

## Required architecture at that scale

- Stateless API fleets across multiple regions, behind health-aware L7 routing and DDoS protection.
- Supabase Auth-compatible JWT validation and a carefully bounded capability gateway. Do not put the service-role key in the browser or use it to bypass RLS.
- PostgreSQL as the authoritative store initially, with PgBouncer/connection pooling, query budgets, replicas for eligible reads, backups, PITR, and tested restores.
- At high write volume, explicit partitioning/sharding strategy for posts, activity, follows, and federation data. Do not shard merely by account count; select keys from measured access patterns and consistency requirements.
- Asynchronous queues and independent workers for feed fanout, federation delivery, notifications, recommendations, indexing, and media processing. Jobs must be idempotent and have retry/dead-letter controls.
- Redis or another purpose-built cache for bounded, non-authoritative hot data, with explicit invalidation/TTL policies.
- The Testagram CDN for media delivery with regional edge caches and shields. Media bytes must not transit the API service.
- Independent search infrastructure only after current PostgreSQL search performance and consistency requirements are measured.
- Per-region observability, SLOs, abuse limits, deployment rollback, disaster recovery, and data residency controls.

## Staged validation

1. Establish the baseline on staging using realistic synthetic accounts and data.
2. Run the included k6 capability test at 1x, 2x, then 5x expected peak; record p50/p95/p99, errors, RPS, CPU, memory, pool waits, PostgreSQL load, and cost.
3. Add mixed scenarios for authenticated feed reads, search, post writes, follows, and background work using test-only JWTs.
4. Test cache-cold and cache-warm states, slow upstreams, DB failover, worker backlog, and regional loss.
5. Increase capacity only after bottlenecks are fixed and the same test passes repeatedly.
6. Do not claim 500M capacity until deployed multi-region infrastructure meets an agreed workload and SLO in reproducible tests.

## k6 smoke/load test

Run against staging, never a production write path:

    API_BASE_URL=https://staging-api.testagram.site MAX_VUS=50 k6 run tests/load/capability.js

Increase `MAX_VUS` gradually. The included script only measures the public health capability path; it is a gateway smoke/load test, not proof of full application or 500M-user capacity.
