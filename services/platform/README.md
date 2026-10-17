# Testagram Platform Core

Private service-to-service API for Testagram's cache, atomic rate limiting, Valkey Streams queues, public feature configuration, events and audit records.

## Runtime contract

- Bind only to the private Docker network on port 8095.
- Internal endpoints require `Authorization: Bearer $PLATFORM_INTERNAL_TOKEN`.
- `/healthz` checks process liveness; `/readyz` checks PostgreSQL and Valkey; `/metrics` exposes Prometheus counters.
- Cache entries are disposable and must never be the canonical copy of user data.
- Queue delivery is at-least-once. Workers must be idempotent and acknowledge only after successful processing.
- The internal token must be a long random secret, unique to this environment, and must never reach browsers or mobile apps.

## Database migration

The Compose stack mounts `migrations/001_platform.sql` into PostgreSQL's init directory. PostgreSQL runs init scripts only when initializing an empty data directory. For an existing database, apply the migration explicitly from the repository root:

```sh
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations/001_platform.sql
```

Take and verify a backup before changing an existing database. The migration is designed to be repeatable, but test it against a staging copy first.

## Status and boundaries

This service is now packaged in this repository and is included in the optional self-hosted Compose stack. The provider-independent client lives in `internal/platformclient`. Application call sites still need to be migrated to that client and tested before the cache/queue API becomes a critical production dependency. The hosting control-plane OpenAPI file is a cross-repository contract only; this service does not implement domain registration, deployment scheduling, or registrar/DNS writes.


## Durable PostgreSQL jobs

Use `/v1/jobs/*` for durable work that must survive Valkey restarts or queue trimming. Jobs are stored in PostgreSQL and support bounded retries, delayed availability, idempotency keys, worker leases, and a terminal `dead` state. Endpoints require the internal bearer token:

- `POST /v1/jobs/enqueue`: `queue`, JSON-object `payload`, optional `idempotency_key`, `max_attempts` (1–20; default 8), and `delay_seconds` (0–604800).
- `POST /v1/jobs/claim`: `queue`, `worker`, optional `count` (1–25) and `lease_seconds` (5–3600). Claims use row locks with `SKIP LOCKED`.
- `POST /v1/jobs/ack`: `id` and the same `worker` value to acknowledge only a lease owned by that worker.
- `POST /v1/jobs/fail`: `id`, `worker`, bounded `error`, and optional `retry_after_seconds`. Retries stop at the configured attempt limit.

Delivery is **at least once**. A worker can finish a job and crash before acknowledging it, so handlers must be idempotent and should use a business-level idempotency key for side effects. A lease expiry allows another worker to retry; it does not prove that the previous worker stopped. Keep lease durations above normal processing time or implement lease renewal before running long media transcodes. Never put credentials or raw private user content in job payloads unless retention, access and redaction policies explicitly allow it.

The migration `005_durable_jobs.sql` is mounted for fresh databases only. Existing volumes must be backed up and the migration applied manually after staging validation. The older Valkey Streams endpoints remain available for ephemeral/high-throughput work; use PostgreSQL jobs when losing a queued item is unacceptable. This is a queue API foundation, not yet a deployed feed/media worker fleet.
