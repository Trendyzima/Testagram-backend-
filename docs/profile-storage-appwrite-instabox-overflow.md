# Appwrite-first profile image storage with Instabox overflow

## Desired behavior

Profile pictures and avatars use Appwrite Storage first. When the Appwrite profile bucket reaches its configured safe threshold, new uploads automatically go to the private Instabox VPS object store. Existing images remain at their original tier and are served through the Backend/CDN abstraction; the app must not assume all profile images are in Appwrite.

## Capacity policy

The 2 GB value is an operator-configured project/bucket budget, not a hard-coded guarantee that every Appwrite plan or project exposes exactly that quota. Configure `APPWRITE_PROFILE_STORAGE_LIMIT_BYTES` from the actual Appwrite console/plan. Default the overflow threshold to 85% of that configured limit, preserving 15% headroom for outstanding uploads, metadata/usage drift and operational safety. The selector counts both verified used bytes and active reservations. The capacity ledger is reconciled against Appwrite Storage on a schedule and after upload/delete events. If reconciliation is stale or Appwrite usage cannot be established, fail safe to Instabox for new profile uploads; do not keep writing blindly to Appwrite.

## Upload flow

1. Authenticated Flutter client asks Backend for a profile-image upload intent. The client never chooses a provider or supplies a storage tier.
2. Backend validates the Appwrite session and canonical Testagram owner UUID; checks image MIME, decoded image dimensions, byte limit, and quota.
3. Backend atomically reserves capacity and selects Appwrite or Instabox VPS using the capacity policy.
4. Appwrite tier: Backend uploads to a private Appwrite bucket with server-only API credentials and records Appwrite file ID. Instabox tier: Backend issues a short-lived, tenant-scoped upload capability for private VPS object storage.
5. Backend verifies stored bytes, SHA-256, dimensions and MIME before marking the image ready and updating profile metadata.
6. Backend returns an opaque media reference/URL served through Testagram's own CDN. No Appwrite API key or VPS S3 credential reaches Flutter.

## Reads, deletion and migration

Profile records store a storage tier plus opaque object reference. CDN/Backend resolves the correct private origin; public profile images may be cacheable only after publication policy is applied. Private/account-only images require viewer authorization and must not be shared-cacheable. Delete and replace operations tombstone the old object and decrement quota only after provider deletion succeeds. A background reconciler paginates Appwrite files, compares actual bytes with the ledger, detects orphan files and repairs drift. Existing Appwrite images are not bulk-copied merely because the threshold is crossed.

## Separation from general media

This is the Appwrite profile-image tier only. Posts, video, stories and other durable media use the separate Instabox VPS + Cloudflare R2 + Backblaze B2 replication policy. Appwrite overflow does not replace or bypass that media durability policy.

## Configuration and gates

- Configure the actual Appwrite endpoint, project, bucket ID, server-only API key, and verified storage limit through deployment secrets.
- Configure the Instabox private S3-compatible endpoint and least-privilege profile bucket credentials.
- Enable a reconciler with a maximum usage-staleness window and alerts at 70%, 80%, 85% and 95%.
- Test concurrent reservations at the threshold, Appwrite quota exhaustion, provider outage, retry/idempotency, checksum mismatch, delete/replace and CDN reads from both tiers.
- Do not enable production routing until Appwrite bucket ACLs, VPS private networking, object lifecycle and restore tests pass.

The current commit introduces the tier-selection policy and PostgreSQL ledger only. The Appwrite upload adapter, scheduled usage reconciliation, profile endpoint integration, and CDN dual-origin resolver are still implementation gates; this is not yet an active automatic failover.
