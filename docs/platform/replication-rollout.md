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


## Testagram public media producer (follow-up implementation)

The additive migration `migrations/20261019_testagram_media_replication_producers.sql` adds a transactional source producer for `testagram_media.assets`. Apply it only after reviewing a current backup and confirming the dual-backend replication foundation migration has been applied.

- Public media is eligible only when status is `ready`, visibility is `public`, checksum is a 64-character SHA-256 hex digest, the content type is allowlisted, and size is within 500 MiB.
- The media completion handler now persists the checksum only for virtual-storage objects after provider-side checksum verification. It does not persist signed URLs or provider credentials.
- Private media never emits a payload. A previously public asset becoming private, quarantined, deleted, or otherwise ineligible emits a tombstone.
- Event entity IDs are stable MD5 identifiers of the object key so the path-like storage key is not used as a protocol identifier; the projection payload separately carries the relative object key.
- The migration creates `platform_replication_health`, a count/age-only view for pending outbox/inbox work, dead letters, and rejected inbox events. It intentionally does not expose event payloads.

This closes only the Testagram media-metadata producer path. It does **not** copy media bytes, establish public CDN access on the receiving backend, or implement account/post authoritative writes. Account and post product writes still need schema-specific local authoritative stores and handlers; the existing capability gateway's Supabase forwarding must not be bypassed until those routes have been migrated with compatibility tests.

Suggested alert thresholds for an exporter polling the health view:
- pending outbox or inbox oldest age above 120 seconds for 5 minutes;
- any increase in outbox dead letters;
- any increase in rejected inbox events;
- persistent nonzero pending counts with no successful delivery/application progress.

Do not enable cross-backend production relay solely because this migration is deployed. First test trigger behavior on a disposable database, validate a public create/update/tombstone end-to-end, confirm private media produces no event, and test a peer outage followed by recovery.


## Public projection read route

The API now exposes `GET /api/replication/public/{origin}/{entity_type}/{entity_id}` for the public mirror only. It accepts the two configured protocol origins and the `account`, `post`, and `media` projection types; it returns 404 for absent or tombstoned records and 503 if the projection database is unavailable. Responses use `Cache-Control: no-store` until public-cache invalidation is integrated. Existing product routes are unchanged, so this is a safe read surface for incremental client integration—not yet a replacement for legacy feed/profile/media queries.


## Local Testagram account/post authority (feature-gated)

The additive migration `migrations/20261019_testagram_local_accounts_posts.sql` creates `testagram_local.accounts` and `testagram_local.posts`. These are real local PostgreSQL tables, and row triggers append versioned account/post events to `platform_replication_outbox` in the same transaction as each write. Account protection/deactivation also causes public post tombstones; private profiles do not emit public account events.

The existing `/api/capability` gateway can route these capability names to the local store when `TESTAGRAM_LOCAL_PRODUCT_WRITES_ENABLED=true`:
- `testagram.profile.update` (full profile payload, including username);
- `testagram.posts.create`;
- `testagram.posts.update` (full post payload, including ID and visibility);
- `testagram.posts.delete` (post ID).

**The feature flag defaults to false and must remain false until validation and client compatibility tests pass.** Local writes require a PostgreSQL connection and an HS256 bearer token that can be verified with `SUPABASE_JWT_SECRET`; asymmetric JWTs are deliberately rejected by this first implementation. If the deployment uses an asymmetric Supabase signing key, do not enable the flag until a proper JWKS verifier is implemented. A local profile must be created before that account can create local posts. These new local tables are not automatically backfilled from Supabase; do not switch existing users over without a verified, resumable migration and conflict policy.

CI now includes an isolated PostgreSQL test that applies the migrations and verifies account/post outbox writes, privacy tombstones, and exclusion of private accounts. Public-projection tombstones are terminal for a given entity ID; if a post was public and later became private or its author became protected, republishing it requires a new post ID. This prevents delayed old events from resurrecting content after a privacy transition. This validates schema behavior in CI only; it does not establish production migration, historical backfill, client cutover, or peer-outage recovery.


## Reconciliation command and peer-outage exercise

Run the read-only reconciler from an operator environment that can reach both databases:

```sh
SOURCE_DATABASE_URL='postgres://…' \
DESTINATION_DATABASE_URL='postgres://…' \
RECONCILE_ORIGIN='testagram-vps' \
go run ./cmd/replication-reconcile
```

It compares each source entity's latest outbox version and tombstone state with the destination projection, checks source pending/dead-letter counts and destination inbox pending/rejected/stale counts, emits a JSON report, and exits non-zero if drift or backlog remains. Treat database URLs as secrets; the command does not print them or payloads.

The two-database CI exercise creates local account/post records while delivery is unavailable, verifies events remain durable and retryable, then restores an authenticated TLS receiver and verifies the relay applies both projections and acknowledges the source events. This validates the code path in disposable CI databases, not a production network partition or a real backup restore.
