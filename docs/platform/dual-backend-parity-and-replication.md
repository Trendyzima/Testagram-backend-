# Testagram peer-backend capability parity and replication

## Target

Testagram is a peer backend to the existing XClone hosted backend, not a media microservice that the hosted backend alone calls. It must implement the same product API contract for agreed capabilities and operate independently on its own VPS infrastructure.

## Capability parity

The same versioned OpenAPI contract and black-box tests must run against both backends. Inventory and close gaps across auth/session verification, profiles/follows/blocks, posts/replies/likes/reposts/bookmarks, feed/discovery/search, media lifecycle, notifications, communities/moderation, messaging/live features where present, abuse prevention, export/deletion, health/metrics and recovery. Route presence is not enough: authorization, response schema, errors, pagination, idempotency and data semantics must match.

## Independent operation

- Testagram must serve its locally owned data and accept local writes while the hosted peer is unavailable.
- Peer outages pause replication/cross-backend reads; they must not synchronously block local writes.
- Keep API process, database roles, storage credentials, Redis/queue configuration, monitoring and backups independently controlled.
- Do not depend on the hosted backend's service-role key or privileged API for ordinary Testagram operations.
- Keep virtual storage opt-in until checksum, durable metadata, quota, deletion, CDN/private access and provider integration gates pass.
- Preserve all existing media URLs/keys and product rows. No bulk migration is implied by parity work.

## Replication protocol

Use a durable PostgreSQL transactional outbox written in the same transaction as the canonical entity change. Events include immutable event ID, entity ID, origin backend, entity version, schema version and operation. Receivers persist an inbox/deduplication record and apply idempotently. Use at-least-once delivery, bounded retries, dead-letter queues, replay and reconciliation. Redis/Upstash may accelerate delivery but cannot be the only event store.

Default to one authoritative writer per entity/account. Multi-writer writes require explicit conflict rules; last-write-wins is not acceptable for privacy, blocks, moderation, ownership or deletions. Represent deletions as durable tombstones and replicate them. Rebuildable feeds, counters, search indexes and caches are projections. Media replication requires checksum verification, destination policy checks and metadata reconciliation before references are published.

## Required staging gates

1. Shared contract suite passes on both backends for all declared capabilities.
2. Stop each backend in turn; the other continues local reads/writes and later drains its durable replication backlog.
3. Replay and reorder events; prove idempotency, ordering/version checks, dead-letter recovery and no duplicate rows.
4. Test private-post visibility, block lists, account deletion, moderation tombstones and cross-tenant access.
5. Test media replication with SHA-256, private bucket isolation, signed URL expiry and CDN cache bypass.
6. Restore each backend from its own off-host backup and document observed RPO/RTO.
7. Alert on replication lag, dead-letter backlog, conflicts, queue age, disk pressure, provider errors and failed backups.

## Implementation status

This document defines the target and release gates only. It does not claim that API parity or replication is implemented. Before production activation, inventory the current Go routes and schemas, add the shared contract suite, implement durable outbox/inbox and reconciliation, and test failure/recovery in staging.
