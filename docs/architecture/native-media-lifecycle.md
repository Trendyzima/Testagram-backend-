# Native Testagram media lifecycle

## Active owner

The production media authority is the native API assembled by cmd/api/main.go, backed by PostgreSQL and the Testagram media schema. The former alternate storage worker suite is not part of the supported runtime and has been retired in favor of these native owners.

- Upload intent, bounded upload gateway, completion and download authorization: cmd/api/media.go.
- PostgreSQL asset metadata, upload intents, storage quotas and deletion jobs: testagram_media and testagram_storage.
- Replica placement and transactional copy/delete events: testagram_media.storage_replicas and testagram_media.media_replication_outbox.
- Replication execution: native internal/replication worker.
- Expired upload cleanup and durable object deletion: cmd/api/storage_cleanup_worker.go and cmd/api/storage_deletion_worker.go.
- Identity evidence storage and retention: cmd/api/identity_evidence_storage.go and cmd/api/identity_evidence_retention.go.
- Provider clients and checksum verification: internal/virtualstorage and the approved Data Centre media client.

## Native invariants

1. PostgreSQL is authoritative for upload intents, owner, visibility, quota, asset status, provider placement and deletion jobs.
2. Upload intents are single-use and claimed before the API reads/spools upload bytes.
3. Backend computes the SHA-256 while streaming; client-reported digests are comparison-only.
4. An asset is not ready until the selected provider confirms object size, content type and checksum.
5. Replication is a durable transactional outbox operation. A successful enqueue is not proof that replicas are verified.
6. CDN delivery cannot authorize access. Private media requires Backend authorization; only verified replicas are eligible.
7. Deletion is a durable, idempotent job. Quota and metadata are not released until provider deletion has been verified.
8. Identity evidence is quarantined before deletion; failed provider deletion remains retryable and the evidence stays inaccessible.

## Explicit scope boundary

The retired alternate `freshstorage` implementation also described separate story-expiry, cold-archive and purge workers. Those workers are not silently represented as implemented by the native upload/deletion pipeline. Any future story expiry or cold-archive feature must be designed against native PostgreSQL ledgers, have independent retention/legal-hold policy, and pass restore, checksum and destructive-operation tests before activation. Do not run the archived worker commands or point production at the former external-provider collections.

## Deployment

The only supported backend entrypoint is cmd/api in the self-hosted Testagram deployment. The obsolete deploy/fresh profile, its environment variables and its external-provider OpenAPI contract are removed. Cross-repository public and private API contracts are governed by Trendyzima/testagram-api.