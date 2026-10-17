# Testagram Platform Core

Private service-to-service API for Testagram's cache, atomic rate limiting, Valkey Streams queues, public feature configuration, events and audit records.

## Runtime contract

- Bind only to the private Docker network on port 8095.
- Internal endpoints require `Authorization: Bearer $PLATFORM_INTERNAL_TOKEN`.
- `/healthz` checks process liveness; `/readyz` checks PostgreSQL and Valkey; `/metrics` exposes Prometheus counters.
- Cache entries are disposable and must never be the canonical copy of user data.
- Queue delivery is at-least-once. Workers must be idempotent and acknowledge only after successful processing.
- The internal token must be a long random secret, unique to this environment, and must never reach browsers or mobile apps.

## Background queue delivery guarantees

The platform queue API uses Valkey Streams with consumer groups and persistent AOF enabled by the self-hosted stack. Enqueue does **not** trim streams to a fixed length: silently trimming unprocessed jobs can lose feed ingestion, federation, notifications or media work. New consumer groups start at stream ID `0`, so messages enqueued before the first consumer starts are still visible. Queue payloads are bounded to 64 fields and 256 KiB.

Delivery is **at least once**, not exactly once. Consumers must use an idempotency key (prefer a stable product/job ID), process successfully before acknowledging, and reclaim abandoned pending entries with `/v1/queue/reclaim`. A crash after the side effect but before acknowledgement can cause redelivery. Monitor stream length and pending-entry age, and define an operator-controlled retention/archival policy before sustained high-volume production. Do not add automatic stream trimming unless the system can prove entries are acknowledged or safely archived first.

## Database migration

The Compose stack mounts `migrations/001_platform.sql` into PostgreSQL's init directory. PostgreSQL runs init scripts only when initializing an empty data directory. For an existing database, apply the migration explicitly from the repository root:

```sh
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations/001_platform.sql
```

Take and verify a backup before changing an existing database. The migration is designed to be repeatable, but test it against a staging copy first.

## Status and boundaries

This service is now packaged in this repository and is included in the optional self-hosted Compose stack. The provider-independent client lives in `internal/platformclient`. Application call sites still need to be migrated to that client and tested before the cache/queue API becomes a critical production dependency. The hosting control-plane OpenAPI file is a cross-repository contract only; this service does not implement domain registration, deployment scheduling, or registrar/DNS writes.
