# Virtual Storage Integration Plan

## Purpose

Connect the existing media lifecycle to the virtual-storage policy layer without creating a competing upload API or changing the current client contract. This is an incremental bridge, not a claim that multi-node pooling is already operational.

## Existing authoritative path

- HTTP handlers and client contract: `cmd/api/media.go`
- Existing S3-compatible signer: `internal/media/signer.go`
- Existing durable intent and asset metadata: `testagram_media.upload_intents` and `testagram_media.assets`
- Policy foundation: `internal/virtualstorage/storage.go`
- Existing self-hosted object store: `object-storage` (MinIO) in `deploy/self-hosted/docker-compose.yml`
- Existing lifecycle state migration: `migrations/006_media_lifecycle.sql`

Do not introduce a second set of canonical object records. The existing media tables remain authoritative during the bridge. New logical namespace/provider columns are added with safe legacy defaults; old objects must not be silently moved or re-keyed.

## Sequence

### 1. Metadata bridge

Apply `migrations/008_virtual_storage_bridge.sql`. Record a logical namespace and provider identifier on intents and assets, and add a durable deletion-job table. Keep legacy rows marked `legacy-media` until an explicit, verified migration maps them. Do not persist secrets or signed URLs.

### 2. Provider contract and adapter

Implement the `virtualstorage.Provider` contract behind a concrete S3-compatible adapter. Required operations:

- signed PUT with enforceable size/content-type constraints where supported;
- signed GET with bounded TTL;
- provider-observed HEAD metadata;
- DELETE with idempotent not-found handling;
- paginated LIST with opaque cursors;
- bounded network timeouts and retries for transient errors.

Do not treat ETag as a SHA-256 checksum. If checksum verification is required, use provider checksum metadata or stream the object through a trusted verifier. A presigned PUT whose size limit is only a server-side argument is not a provider-enforced size limit.

### 3. Integrate existing handlers

Adapt the existing upload-intent, completion, private-download, list and delete handlers rather than exposing duplicate routes. Tenant identity comes only from `authenticatedMediaUser`. On completion, load the persisted intent by both object key and owner, reject expired/non-issued intents, HEAD the expected physical object, validate actual size and content type, then atomically transition intent and asset metadata. Repeated completion must be idempotent.

For private downloads, require a ready asset and owner authorization before signing. Public delivery must remain read-only and must not imply upload or deletion permission. Preserve current response JSON fields and routes until client integration tests prove compatibility.

### 4. Durable cleanup

When an upload intent expires, cancellation occurs, moderation quarantines an asset, or a user deletes an object:

1. Commit the metadata state transition and enqueue a deletion job in the same PostgreSQL transaction.
2. A bounded worker claims jobs using `FOR UPDATE SKIP LOCKED`.
3. Delete from the configured provider with an idempotent operation.
4. Mark the job complete only after provider success or confirmed not-found.
5. Retry transient errors with capped exponential backoff; move persistent failures to dead-letter state and alert.
6. Reconcile old intents and orphaned provider objects without deleting anything whose ownership cannot be established.

Never make a database transaction wait on a remote object-store request.

### 5. Release gates

- Unit tests for provider signing, key validation, namespace mapping, error classification and cursor handling.
- Integration tests against MinIO in CI; live R2 smoke tests only when explicitly configured with test credentials.
- Upload -> complete -> private download -> delete round trip.
- Reject cross-tenant reads/deletes, expired intents, oversized objects, unsupported MIME types and metadata mismatches.
- Concurrency tests for quota reservation and idempotent completion/deletion.
- Failure injection for provider timeouts, repeated deletes, worker restarts and PostgreSQL restarts.
- Backup/restore test proving both metadata and object bytes can be recovered.
- Keep the new path feature-gated; do not switch production traffic until all gates pass.

## Later: storage-node pooling

Only after the provider adapter and lifecycle are proven should the platform add node registration and placement. A node must report capacity, free bytes, health, failure domain and protocol version; it must authenticate with a revocable node identity and be restricted to configured storage roots. Placement decisions must be durable and object locations must be recorded before an upload is reported ready.

Start with explicit placement and no cross-node replication. Add replication only after repair/reconciliation logic, checksums, failure-domain awareness and restore tests exist. A storage pool is not resilient merely because it has multiple disks.
