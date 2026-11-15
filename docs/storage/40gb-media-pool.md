# Testagram 40 GiB logical media pool

## Capacity contract

The operator-approved logical media pool is **40 GiB (42,949,672,960 bytes)**. The capacity is charged once per verified logical asset; physical replicas in the InstaBox VPS, Cloudflare R2 and Backblaze B2 consume their own provider capacity and are not counted as three separate user uploads.

PostgreSQL in the InstaBox VPS control plane is authoritative for:
- object identity, owner, visibility and lifecycle state;
- content type, byte count and SHA-256;
- upload reservations and per-user/global logical quota;
- replica provider, bucket/namespace, object key, checksum, verification state and last reconciliation.

Object stores may carry a small versioned metadata sidecar/replica manifest for recovery and provider-local inspection. A sidecar is never authoritative over PostgreSQL and must not contain credentials, signed URLs, private user content or identity evidence payloads.

## Safe capacity migration

Apply `migrations/048_set_media_pool_40gib.sql` after migrations 000–047.

The migration:
- locks the singleton pool row;
- refuses to reduce capacity below already-used plus reserved bytes;
- updates the logical capacity to exactly 40 GiB;
- does not delete media, release reservations, migrate objects, create buckets or provision physical disk.

For an existing PostgreSQL volume, apply through the trusted migration runner or psql after a verified backup. Do not delete the database volume or rely on Docker's first-initialization scripts to update an existing database.

## Provider topology

- **InstaBox VPS / MinIO:** primary ingest and hot copy; 40 GiB physical allocation must be verified against the VPS filesystem/volume before enabling uploads against it.
- **Cloudflare R2:** verified replica with separate private/public namespaces and dedicated private identity-evidence namespace.
- **Backblaze B2:** verified replica with the same namespace isolation.
- Each successful replica requires object size and SHA-256 read-back verification. ETags are not SHA-256.
- Backend authorizes access and issues media capabilities; Go CDN delivers approved bytes. Provider URLs are not exposed as authorization bypasses.

## Weekly space-recovery policy

A weekly maintenance run must first emit a dry-run report. It may reclaim only:
1. expired upload reservations after checking their expiry and upload state;
2. temporary multipart/staging objects with no live upload intent;
3. objects proven orphaned by a complete PostgreSQL reference scan and replica-ledger reconciliation;
4. disposable CDN cache entries (cache is not durable storage).

It must never automatically purge live posts, direct messages, profile/avatar media, identity evidence, moderation/legal holds, or the only verified replica. User-requested deletion follows its own recovery-window policy. An object is eligible for deletion only when ownership, namespace, object key, checksum, all references and all replicas have been reconciled. Unknown provider outcomes remain retryable and charged.

The weekly job must have a unique lease, bounded batches, audit events, provider read-after-delete verification, retry/dead-letter handling, and a report of reclaimed bytes by provider. Destructive execution remains disabled until the staging dry-run, restore drill and deletion safety tests pass.

## Physical capacity is a separate gate

A 40 GiB logical quota does not prove the VPS has 40 GiB available. Before turning on uploads, operators must verify the MinIO data volume's actual filesystem capacity, free-space reserve, filesystem alerts, and the independent R2/B2 bucket credentials and provider quotas. Never treat this migration as physical provisioning.
