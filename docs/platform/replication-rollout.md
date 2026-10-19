# Dual-backend replication rollout (implementation status)

This branch now contains an executable, opt-in replication path for **public account-profile and post projections**. It is still not a complete mirror of every product domain, a production failover system, or proof of disaster recovery.

## Implemented in code

- Shared versioned event envelope and privacy validation in Go and TypeScript.
- Transactional XClone outbox producers attached to `public.profiles` and `public.posts` writes.
- Per-entity source versions, advisory-lock serialization, destination version ledger, and deletion/privacy tombstones.
- Bounded idempotent snapshot seeding for existing eligible public profiles/posts.
- HMAC-authenticated receiver, replay-window checks, body-size limits, lease-token acknowledgements, bounded retries, and terminal dead-letter handling.
- Durable inbox projection workers on both backends. They write to a separate allowlisted public-data mirror, not authoritative product tables.
- Testagram backlog/age/dead-letter metrics, an authenticated XClone projector function, and an opt-in scheduled workflow gated off by default.
- Contract validation and focused Go tests.

## Still incomplete

- Media metadata and binary/object replication are not wired; neither are polls, communities, follows, messages, notifications, moderation, and other product domains.
- Testagram's current capability gateway still forwards product reads/writes to Supabase. Its local Go outbox helper is available for future local transactional writes, but Testagram is not yet an independent authoritative source for all product data.
- The projection mirror is not yet used as a replacement for the existing product query paths. Do not switch user-facing reads to it until schema-specific integration and authorization tests pass.
- Production migrations have not been applied here; no live synchronization, stop-one-backend test, restore drill, or measured RPO/RTO has been verified.

## Safe rollout sequence

1. Take off-host backups and perform a restore drill before touching production.
2. Review and apply the additive Testagram migration and both XClone migrations using the normal deployment pipeline.
3. Run `seed_platform_replication_public_snapshot(100)` repeatedly with service-role credentials until it returns zero; inspect counts and verify no private/community content is included.
4. Configure the HMAC peer secret and the separate relay/projector tokens through the secret manager. Keep `REPLICATION_WORKERS_ENABLED` disabled until endpoint and database checks pass.
5. Run duplicate/reordered/stale/tombstone tests, invalid-signature and expired-timestamp tests, media authorization checks, retry/dead-letter recovery, and source/destination reconciliation.
6. Enable the scheduled worker only after those gates pass, then alert on pending age, backlog growth, and terminal failures.
7. Wire each remaining domain through an atomic source outbox producer and schema-specific destination projector before claiming full parity.

The code preserves existing product tables and legacy media routes. No production migration or runtime cutover is performed by these pull requests.
