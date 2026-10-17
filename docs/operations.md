# Operations and capacity runbook

## Before public deployment

- [ ] Add and review real product routes against existing Testagram API contracts.
- [ ] Add authentication and authorization for every non-health endpoint.
- [ ] Configure TLS and a trusted reverse proxy/load balancer.
- [ ] Restrict database network access; create a least-privilege runtime database role.
- [ ] Store credentials in deployment secrets, rotate exposed credentials and never commit .env.
- [ ] Configure automated database and object-storage backups; test restoration.
- [ ] Add alerting for 5xx rate, p95/p99 latency, DB pool saturation, CPU/memory, disk space and worker backlog.
- [ ] Verify migrations on staging before production.
- [ ] Confirm CDN origin cannot be bypassed for protected media.

## Suggested initial service objectives

These are starting targets to validate, not existing guarantees:

- API availability: 99.9% monthly for critical read paths.
- API latency: p95 below 300 ms for simple cache/database reads, excluding media transfer and third-party federation.
- Error rate: below 0.5% for ordinary requests during a sustained test.
- Database pool wait: near zero under expected load.
- Background queue age: bounded by each job's product requirement.

Tune targets based on real usage and cost.

## Load-test progression

Run tests against staging, with synthetic data and explicit authorization boundaries. Start at 1x expected peak, then 2x and 5x; do not jump straight to millions of virtual clients.

At every stage record:
- requests/second and successful throughput;
- p50/p95/p99 latency and timeout/error rate;
- active HTTP/WebSocket connections;
- API CPU, memory, restarts and goroutine count;
- PostgreSQL CPU, slow queries, pool usage/wait, locks and I/O;
- cache hit ratio and CDN egress;
- queue depth, oldest job age, retries and dead-letter count;
- external bandwidth and per-request cost.

Stop and fix regressions before increasing load. Use realistic mixes of feed reads, post creation, likes, profile reads, search, login, notifications and federation activity. Do not use real users' credentials or production write traffic for load testing.

## Incident checklist

1. Identify whether the bottleneck is API, database, queue, origin or network.
2. Protect the source of truth: reject/queue noncritical work rather than exhausting the database.
3. Check recent deploys, pool saturation, slow queries, cache misses and dependency health.
4. Roll back unsafe releases; avoid blind retries of non-idempotent writes.
5. Preserve logs and metrics, resolve the root cause, then run a regression test.
6. Document impact, timeline and prevention actions.

## Local checks

    go test -race -count=1 ./...
    go vet ./...
    gofmt -w cmd
    docker compose config --quiet

The local Compose database is disposable development infrastructure. It is not a production backup or high-availability configuration.
