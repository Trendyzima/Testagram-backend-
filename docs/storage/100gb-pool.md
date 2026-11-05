# Testagram 100 GiB media pool

## Scope

This change adds the PostgreSQL control-plane foundation for a 100 GiB logical media pool. It does not create physical storage, provision provider accounts, migrate existing objects, or automatically enable new providers.

- Logical quota is charged once per verified asset.
- Physical replicas are tracked separately and require additional physical capacity.
- Default per-user quota is 5 GiB; the global pool is the aggregate hard cap.
- Upload reservations are transactional and safe across concurrent API instances.
- Existing ready assets are included in a one-time usage backfill.
- Node and replica inventory are metadata only. A node is not upload-eligible until a trusted heartbeat and placement protocol is deployed.

## Migration

Apply migrations/011_global_storage_pool.sql after migrations 000–010.

For an existing PostgreSQL deployment, take a verified backup first, then apply using the project's migration process. The self-hosted Compose bootstrap mounts migrations only when the database volume is first initialized; existing volumes need the migration runner or psql explicitly. Do not delete the database volume to apply it.

## Capacity policy

Default global capacity: 107374182400 bytes (100 GiB).
Default per-user quota: 5368709120 bytes (5 GiB). Operators can adjust a user's quota through a trusted administrative SQL operation. Do not expose these tables or functions directly to browser/mobile clients.

The upload-intent insert trigger reserves the declared maximum bytes before a signed upload can proceed. A global or user quota overflow aborts the insert with SQLSTATE 53100 and a stable message: TESTAGRAM_GLOBAL_STORAGE_LIMIT or TESTAGRAM_USER_STORAGE_LIMIT. When a verified asset row is inserted, its actual bytes replace the reservation. Cancelled/expired intents release reservations. Asset status changes away from ready and hard deletion decrement logical usage.

The API should translate these quota messages to HTTP 507 with machine-readable error codes. Do not return raw SQL error details to clients.

## Abandoned uploads

Run SELECT testagram_storage.expire_abandoned_reservations(500); from a trusted scheduled worker. It expires only reservation rows whose intent expiry has passed. Existing object cleanup remains the deletion worker's responsibility. Do not release a reservation merely because a node heartbeat is missing.

## Node health and placement rules

The testagram_storage.nodes table stores capacity, used/reserved bytes, free-space floor, status and last heartbeat. Placement policy must require:
- status = online;
- heartbeat newer than the configured freshness window (recommended 90 seconds);
- capacity minus used and reserved bytes is at least the minimum free-space floor plus requested bytes;
- node is not disabled and its provider credentials are present in server-side configuration.

Mark stale nodes offline in the scheduler. Never treat a phone or laptop as durable storage unless it has an always-on authenticated agent, signed heartbeat, disk-space report and explicit availability policy. Never expose host filesystem paths or arbitrary path selection.

## Checksums, ownership and replicas

Existing media verification remains authoritative. SHA-256 must be verified against stored bytes; ETag is not a cryptographic checksum. Keep object ownership tied to the authenticated upload-intent owner ID. Replica rows should be marked verified only after destination size and SHA-256 verification. Pending/failed replicas are not safe copies.

## Provider routing and caching

The existing S3-compatible provider can be configured for R2, B2 S3 API, or MinIO using server-side credentials. This migration does not yet implement automatic multi-provider failover or direct browser upload to each provider. Preserve current provider selection until provider-specific integration tests pass.

For public media, use a custom media hostname proxied through Cloudflare only after cache rules, origin access policy and cache-key behavior are tested. Private media must remain private and use short-lived signed URLs; never cache private responses publicly. Use immutable cache headers only for versioned public objects.

## Operational acceptance tests

Before enabling the pool in production:
1. Apply migration to a staging copy and verify pool_status equals the sum of ready assets.
2. Run concurrent reservations whose total exceeds remaining capacity; only reservations that fit may commit.
3. Verify per-user quota independently of global quota.
4. Verify actual uploaded size cannot exceed reserved size.
5. Verify cancelled and expired intents release reservations once.
6. Verify duplicate completion cannot double-consume usage.
7. Verify asset deletion and status transitions adjust counters exactly once.
8. Verify offline and low-space nodes are excluded by placement service.
9. Verify replica checksums and restore a sample object from a replica.
10. Load-test API and provider upload path separately.

## Important limitations

This is a schema/control-plane foundation, not a production-ready distributed storage service by itself. Application-level provider routing, authenticated node heartbeats, node placement, replication workers, Cloudflare cache integration, quota-specific HTTP error mapping and automated migration execution remain follow-up implementation work. No provider free tier is guaranteed to supply 100 GiB; the configured pool must be backed by actual capacity.
