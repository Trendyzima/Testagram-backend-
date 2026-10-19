# Dual-backend replication rollout (implementation status)

This change introduces the first executable replication primitives, not an enabled
cross-backend sync path.

## Included in this change

- A versioned event envelope and validation in `internal/replication`.
- PostgreSQL outbox leasing with `FOR UPDATE SKIP LOCKED`, bounded retries, and
  an idempotent inbox receipt table.
- Additive outbox/inbox tables in the Testagram migration and the XClone Supabase
  migration.
- A shared JSON Schema and a runnable XClone contract validator.
- Replication queue tables are not granted to public SQL roles.

## Apply the schema safely

1. Take and verify a current database backup.
2. On the Testagram database, review and apply
   `migrations/20261019_dual_backend_replication.sql` using the deployment's
   migration process (or `psql -v ON_ERROR_STOP=1 -f ...` in a controlled
   maintenance session).
3. On the XClone Supabase project, deploy the additive migration through the
   repository's normal Supabase migration process.
4. Verify that `platform_replication_outbox` and
   `platform_replication_inbox` exist and are not readable by browser roles.
5. Keep replication disabled until both database migrations are applied and
   the peer-authentication, receiver, projection, replay, and reconciliation
   gates have passed.

The migration does not modify existing product tables or copy existing content.
It does not turn on replication or alter existing media routing.

## Required next integration steps

- Emit an outbox event in the same transaction as each authoritative entity write.
- Add a mutually authenticated peer transport and receiver with request-size and
  timestamp/replay bounds.
- Apply inbox receipt and the domain projection in one transaction; reject stale
  entity versions and preserve tombstones for deletes, blocks, and moderation.
- Add durable relay scheduling, dead-letter/replay tooling, lag metrics, and
  reconciliation against source-of-truth records.
- Add conformance tests against both live implementations and stop-one-backend
  fault-isolation tests.

Until those steps are implemented and verified, the outbox is a foundation only:
it must not be described as live replication or disaster recovery.
