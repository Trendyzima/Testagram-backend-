# Testagram Platform Core

Private service-to-service API for PostgreSQL-backed feature configuration,
event telemetry and audit records.

## Runtime contract

- Bind only to the private Docker network on port 8095.
- Internal endpoints require `Authorization: Bearer $PLATFORM_INTERNAL_TOKEN`.
- `/healthz` checks process liveness; `/readyz` checks PostgreSQL; `/metrics`
  exposes write counters and database pool gauges.
- PostgreSQL is the only persistence dependency.
- No cache API, Redis/Valkey, Upstash, generic queue, or cache wrapper is part
  of the supported runtime.

Durable asynchronous product work must be recorded in the owning service's
PostgreSQL transaction/outbox and processed with bounded leases and idempotent
workers. The platform service does not provide a generic message queue.
