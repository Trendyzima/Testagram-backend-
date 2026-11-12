# Media object replication runbook

## Current implementation

The Backend now has an opt-in, durable object-replication path for virtual-storage media:

1. The canonical `testagram_media.assets` write is verified against the primary Instabox VPS object store before metadata is persisted.
2. A PostgreSQL trigger writes a replication or deletion job in the same transaction as the asset lifecycle transition. No Redis queue or best-effort in-memory enqueue is involved.
3. The API process runs a bounded worker that leases jobs with `FOR UPDATE SKIP LOCKED`. Expired leases are reclaimable after a crash.
4. The worker verifies the primary object's size, content type and SHA-256, then streams it to Cloudflare R2 and Backblaze B2. Each provider copy is read back and SHA-256 verified by the S3 provider implementation.
5. The worker records all three verified placements in `testagram_media.storage_replicas` and marks the job complete in one PostgreSQL transaction. Every 15 minutes it also schedules a reconciliation scan for missing, degraded or older-than-24-hour placements. Reconciliation performs destination HEAD and SHA-256 checks, and repairs only missing or drifted copies rather than blindly re-uploading healthy objects.
6. Retryable failures use capped exponential backoff. After 20 attempts the job becomes a dead letter; the stable error code is stored without response bodies, credentials, signed URLs or object bytes.
7. Deletion and quarantine transitions enqueue a delete operation. The worker removes the tenant-scoped object from all three providers and records a deleted placement state where the asset row remains.

The queue and placement ledger are private server-side metadata. Public-only cross-backend metadata replication remains a separate mechanism and never exports private object details.

## Enablement contract

Keep `MEDIA_REPLICATION_ENABLED=false` until all three providers are provisioned and the end-to-end object-origin suite has passed.

To enable the worker, configure:

- `VIRTUAL_STORAGE_ENABLED=true`
- `MEDIA_REPLICATION_ENABLED=true`
- The existing `MEDIA_S3_*` primary Instabox VPS endpoint and credentials
- Independent `CLOUDFLARE_R2_*` endpoint, access key, secret and private/public buckets
- Independent `B2_S3_*` endpoint, access key, secret and private/public buckets

The API refuses to start with the worker enabled if the primary provider or either replica provider is incomplete. Do not reuse credentials between providers. Use separate private and public buckets at each provider.

**Important limitation:** this worker is asynchronous. It guarantees durable enqueue and eventual retry, but it does not yet hold media publication or CDN delivery until all three placements are verified. Do not describe the system as three-copy-available or failover-ready until the delivery/readiness gate and cross-provider origin tests are complete.

## Applying migrations to an existing database

A new PostgreSQL volume receives the migrations through the self-hosted Compose init mounts. Existing volumes do not rerun those mounts. For an existing installation, use:

```bash
bash deploy/self-hosted/apply-media-replication-migrations.sh
```

The script creates an off-container custom-format backup, verifies its checksum and archive listing, requires the explicit confirmation string, then applies and verifies the replica ledger and transactional outbox. Retain the backup until a restore drill has succeeded.

## Operational checks

Queue health (counts and age only):

```sql
SELECT * FROM testagram_media.media_replication_health;
```

Inspect recent job outcomes without selecting payloads or credentials:

```sql
SELECT status, operation, count(*), max(attempts), min(created_at)
FROM testagram_media.media_replication_outbox
GROUP BY status, operation
ORDER BY operation, status;
```

Inspect verified and deleted placements:

```sql
SELECT provider, state, count(*), max(updated_at)
FROM testagram_media.storage_replicas
GROUP BY provider, state
ORDER BY provider, state;
```

Dead letters require an operator investigation before requeue. Confirm the primary object still exists and its checksum matches the canonical asset row, verify the target provider's bucket/credentials/quota, then requeue only the affected row:

```sql
UPDATE testagram_media.media_replication_outbox
SET status='pending', attempts=0, available_at=now(), failed_at=NULL,
    lease_until=NULL, lease_token=NULL, last_error_code='', updated_at=now()
WHERE id = :job_id AND status='dead_letter';
```

Never mass-reset dead letters without identifying the underlying provider failure.

## Release gates still outstanding

- Run disposable MinIO-origin tests for success, wrong checksum, missing primary, R2 outage, B2 outage, partial-copy retry and process restart during an active lease.
- Verify the three provider objects independently using SHA-256 read-back, not ETags.
- Test deletion/quarantine propagation and retry after one provider is unavailable.
- Extend reconciliation with provider-listing sweeps for untracked orphan objects and verify recovery after credentials or provider endpoints change.
- Add the media delivery readiness gate so private/public delivery cannot claim replica availability before all required placements are verified.
- Perform an independent offsite backup and full restore drill; measure and record RPO/RTO.
- Only then consider enabling the worker in production. The CI schema test is not a production provider-origin test.
