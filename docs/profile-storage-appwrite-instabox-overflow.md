# Profile metadata and media-object storage contract

## Responsibility split

Appwrite is used for application metadata and account/document services. It is **not** a binary media store, media origin, overflow bucket or CDN origin. Do not use Appwrite Storage, Appwrite's S3-compatible endpoint, Appwrite file APIs, or an Appwrite file quota as part of avatar, image, video or document byte placement.

Media bytes are stored in the object-storage system:
- Instabox VPS object storage is the current primary write origin for the Backend's virtual-storage media path.
- Cloudflare R2 and Backblaze B2 are independent replicas, copied and read-back verified by the durable Backend replication worker.
- The Testagram Go CDN is the viewer-facing delivery layer. It resolves authorized origins and must not expose bucket credentials or bypass Backend media authorization.

Appwrite/Backend records may store only metadata and references: opaque object ID/key, owner, visibility, MIME type, byte count, checksum, storage namespace/provider, lifecycle state, and timestamps. They must never contain the media bytes, base64 file payloads, signed upload URLs, or provider credentials.

## Avatar upload and read lifecycle

1. Flutter sends the avatar to the authenticated Backend profile-media endpoint. The client does not select a provider or physical bucket.
2. Backend validates the caller identity, owner UUID, image signature/MIME, maximum 2 MiB size, and per-user quota.
3. Backend allocates a tenant-scoped opaque object key and records an upload intent in PostgreSQL.
4. Backend writes the binary object to the configured private Instabox VPS object origin and verifies provider-observed size, content type and SHA-256.
5. Backend commits the asset metadata and profile's opaque avatar reference. The transactional outbox schedules R2 and B2 replicas.
6. The replication worker streams the bytes to Cloudflare R2 and Backblaze B2, independently reads each copy back, and verifies SHA-256 before recording the replica placement.
7. Profile reads return a CDN URL or opaque media reference. The Go CDN enforces Backend-issued viewer authorization and selects only verified, policy-eligible origins.
8. Replacement/deletion updates metadata transactionally and queues object deletion/replica cleanup. Never delete the previous avatar before the replacement metadata transaction succeeds.

## Storage and delivery invariants

- Appwrite is metadata-only. No Appwrite Storage bucket is required for Testagram media.
- The binary media path is Instabox VPS + Cloudflare R2 + Backblaze B2; each provider has independent credentials and private/public bucket mappings.
- The VPS is the current primary in the Backend replication worker. R2 and B2 are replicas, not assumed interchangeable merely because credentials exist.
- Public/private object namespaces and bucket policies remain separate at every provider.
- Private media requires a Backend authorization assertion bound to object key, action/scope, expiry and intended audience. A caller-controlled `private=1` query parameter is never authorization.
- Never mark a media object three-copy protected or replica-available until both independent copies have passed read-back SHA-256 verification.
- CDN origin selection may use a replica only when its placement ledger says verified and its checksum/size match the canonical asset.
- Signed URLs, provider credentials and API keys are server-side only. No B2/R2/VPS secret belongs in Flutter configuration or Appwrite documents.

## Quota and capacity

The 2 GiB per-user profile-media quota is a Testagram application quota recorded in the canonical Backend metadata ledger; it is not an Appwrite Storage quota. Reservations count against the quota before object upload. Expired intents and deletion jobs must release reserved/used bytes only after their lifecycle transition is verified.

## Current implementation status

The Backend profile-avatar handler currently writes bytes to the private Instabox VPS origin, verifies size/type and SHA-256, stores the avatar reference in Backend metadata, and queues deletion of the replaced VPS object. The durable replication worker is designed to copy ready media to R2 and B2 with checksum read-back verification.

The following are still release gates, not assumed complete:
- Verify the avatar upload creates the expected transactional replication outbox event and that both R2 and B2 replicas reach `verified`.
- Gate CDN delivery/readiness on the correct placement policy; asynchronous enqueue alone does not prove three-copy availability.
- Complete Backend-issued CDN assertion integration for profile avatar URLs and verify public/private cache behavior.
- Exercise B2, R2 and VPS outage/recovery against real provider origins, including checksum mismatch, retry, delete and restore.
- Confirm all live provider endpoints, regions, bucket names, CORS and least-privilege credentials through deployment secret stores; do not commit secrets.
- Appwrite stores the profile/account metadata and opaque object reference only. No Appwrite file-upload or automatic Appwrite-to-VPS overflow path is part of this design.
