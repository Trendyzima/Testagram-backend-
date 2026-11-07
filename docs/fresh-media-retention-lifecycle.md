# Testagram media retention and lifecycle design

## Approved policy proposal

| Content | Policy | User-visible behavior |
|---|---|---|
| Normal posts | Permanent | Post text and engagement remain indefinitely. Attachments may be archived after 30 days. |
| Stories | Expire after 24 hours | Hide at expiry; delete unshared story media only after reference checks. |
| Ordinary media | Archive after 30 days | Preserve media and attachment references; retrieval may require restore. |
| User-deleted media | 30-day recovery window | Revoke access immediately; purge all copies only after the recovery deadline. |

This is an implementation plan, not authorization to run destructive jobs. Retention remains disabled and dry-run by default. No old Supabase data is read, migrated, changed, or deleted.

## Non-negotiable invariants

1. No object is deleted until its identity, owner, namespace, byte count, MIME type, SHA-256, and complete replica inventory have been loaded from the fresh Appwrite ledger.
2. Never delete the only verified copy. Archive first, read back the archive object, verify byte count and SHA-256, persist the verified archive replica, and only then remove active copies.
3. A provider timeout is an unknown outcome, not proof of deletion. Re-HEAD and reconcile before retrying.
4. Never release quota while any charged copy remains, any deletion is unverified, or the replica inventory is incomplete.
5. Jobs are deterministic and idempotent. A repeated run must converge on the same terminal state without duplicate quota changes.
6. Claims use Appwrite transactions and expiring leases. A stale worker cannot overwrite a newer claim or complete a job it no longer owns.
7. Every state transition and provider operation is auditable. Errors are bounded/redacted; never log signed URLs, credentials, or user content.
8. The retention worker never processes direct messages, avatars, profile media, or ordinary post text unless a separate explicit policy is approved.

## Lifecycle state machines

### Story expiry (24 hours)

`active -> expired -> media_reference_check -> delete_pending -> deleting_replicas -> deleted`

- Compute `expires_at` once at story creation as UTC `created_at + 24h`; never recompute it on retries.
- At or after the deadline, transactionally claim the story and mark it expired. Feed/API reads must reject expired stories even if the cleanup worker is delayed.
- For every attachment, query all live references. If another post/story/message/profile references the same object, detach only the expired story reference and retain the object.
- Otherwise create a deterministic `expire_story` job.
- Delete B2, R2, archive, and node copies only after all are listed in the ledger. Verify each is absent. Missing objects are treated as already deleted only after a successful provider HEAD confirms not-found.
- Release logical quota exactly once after all required copies are confirmed absent and the media ledger is terminal.

### Archive after 30 days

`active -> archive_pending -> copying -> verifying -> archived -> source_cleanup -> archived`

- Eligibility is based on media `created_at + 30d`, not post edits or worker execution time.
- Require a configured archive destination and a complete source-replica ledger.
- Select a healthy source replica, validate metadata, stream/copy to archive, then independently HEAD and SHA-256 the archive object.
- Persist the archive replica as verified before deleting any active copy.
- If copy, read-back, checksum, Appwrite commit, or source deletion fails, keep every remaining good copy and retry. Never delete the source to make the job appear successful.
- Keep `media_objects`, `post_media`, and post text intact. The download path must resolve archived media and support a restore-to-hot workflow.
- Restore requests are coalesced by object ID; restore to an active provider, verify, then serve. Do not create multiple restores for concurrent requests.
- Important: a second B2/R2 copy is replication, not cold archival. Cloudflare R2 is not itself a cold-storage class. Do not claim cost savings until an actual lower-cost archive destination and retrieval behavior are configured.

### User-deleted media (30 days)

`ready -> delete_pending -> recovery_window -> purge_claimed -> deleting_replicas -> deleted`

- User action immediately revokes signed URL issuance and hides the attachment; record `deleted_at` and immutable `delete_after = deleted_at + 30d`.
- Keep all recoverable copies during the window. The object is not served while deleted, except through an authenticated restore action by its owner.
- Restore before the deadline is a transaction that clears the pending purge, restores attachment visibility, and invalidates/revokes any outstanding deletion claim. The worker must re-read state before every destructive operation.
- At/after the deadline, claim a `purge_deleted` job. Recheck the deadline, owner deletion state, legal/abuse holds, and references inside the claim transaction.
- Delete every ledgered B2, R2, archive, and node replica. Verify each deletion. A provider outage or unknown response leaves the job retryable and quota charged.
- Only after every replica is confirmed absent: mark the media tombstone terminal, remove/expire attachment references as appropriate, and decrement user/platform used quota once in the same Appwrite transaction that marks the job completed.
- Preserve a minimal non-content tombstone/audit record to prevent resurrection by delayed retries.

## Worker execution model

- Run from a trusted backend scheduler (QStash or controlled cron), never from the browser or Android node.
- Separate queues/worker commands: story-expiry, archive, deleted-media-purge, and retry/reconciliation.
- Each invocation processes a bounded batch (default 100), uses a 20-minute execution budget, and exits non-zero if any item remains failed/retryable.
- Use a unique lease owner per process. Claim pending jobs transactionally with `lease_until`; reclaim only after lease expiry. Renew leases during long object transfers.
- Use exponential backoff with jitter and a maximum retry delay. Do not discard permanently failing jobs; move them to `blocked` with an alert and operator recovery path.
- Every provider action has an operation key derived from job key, provider, and operation. Provider delete is treated as idempotent, but always followed by HEAD verification.
- Reconciliation periodically compares Appwrite replica records with provider objects, including orphan detection. Orphans are not deleted automatically until ownership and age are proven.

## Appwrite records and indexes

The schema extension is in `fresh-appwrite-database-schema.md`:
- `stories`: status and indexed `expires_at`.
- `media_objects`: retention class, lifecycle state, archive/delete deadlines, policy version, lease, attempt count, last access.
- `media_lifecycle_jobs`: deterministic job key, action, state, deadline, lease, retry count, error and timestamps.
- `media_lifecycle_audit`: append-only per-operation audit trail.
- `storage_replicas`: one record per provider/bucket/key, including archive and node providers.

Provision all attributes and indexes before enabling workers. Appwrite attribute/index operations are asynchronous; wait until each is available.

## Rollout and no-retroactivity guard

1. Deploy schema only; no workers enabled.
2. Set `TESTAGRAM_RETENTION_ENABLED=false`, `TESTAGRAM_RETENTION_DRY_RUN=true`.
3. Set `TESTAGRAM_RETENTION_START_AT` to the UTC deployment/approval cutoff. The worker must refuse to start if it is empty or invalid.
4. Dry-run scans only new records at/after the cutoff and emits candidate counts, bytes, reference counts, and intended actions. It performs no writes to posts, stories, media, replicas, or quotas.
5. Review sample candidates and archive destination verification, then explicitly approve enabling each job class.
6. Enable story expiry first, then archive, then user-deletion purge. Keep independent kill switches.
7. Monitor oldest pending job, retry age, bytes pending archive, bytes pending purge, verified archived bytes, failed checksum count, quota reclaimed, and provider deletion verification.
8. Roll back by disabling workers. Do not reverse completed user deletion; restore only from the recovery window or verified archive/backup.

## Acceptance tests

- Exact 24-hour story boundary; UTC and daylight-saving-independent calculations.
- Story with shared attachment does not delete media still referenced by a permanent post.
- Permanent post never receives a delete deadline.
- Archive destination outage, partial copy, wrong size, wrong SHA-256, and Appwrite write failure never delete source.
- Repeated archive run creates one archive replica and one completed job.
- Restore racing with purge prevents deletion if restore wins the transaction; purge rechecks state before every provider delete.
- Purge exactly at 30 days, one nanosecond before deadline, and after deadline.
- Provider delete timeout followed by HEAD found/not-found reconciles safely.
- Partial replica inventory blocks quota release.
- Concurrent workers cannot claim or complete the same lease.
- Quota decrement is exactly once, transactionally coupled to terminal job state.
- Dry-run produces no mutation requests.
- Records created before the cutoff, legacy Supabase data, DMs, avatars, and unrelated media are never selected.

## Current implementation boundary

The existing fresh storage code already handles upload reservation expiry and B2/R2 verification/repair. That reservation expiry is not post/story retention. This design adds the separate content lifecycle and must not reuse reservation cleanup to delete ready media. Archive-provider selection, Appwrite transaction implementation, story integration, restore delivery, and scheduled worker deployment remain required before production enablement.


# Testagram backend retention worker implementation boundary

The retention policy and schema are defined in `fresh-media-retention-lifecycle.md`. This commit adds a pure policy evaluator with deterministic job identities and tests for cutoff, exact 24-hour expiry, permanent-post archive-only behavior, and the fresh user-deletion event cutoff.

## Worker architecture required
- `fresh-story-expire`: claim due story records, mark expired before media cleanup, detach shared references safely, and enqueue object cleanup only when no live references remain.
- `fresh-media-archive`: after 30 days, copy to a separately configured archive provider, verify metadata and SHA-256 by independent read-back, persist the archive replica, then clean active replicas.
- `fresh-media-purge`: after the 30-day recovery window, purge user-deleted objects across every ledgered provider/node replica, verify absence, then release quota exactly once.
- `fresh-media-restore`: coalesce concurrent restore requests, copy from archive to active provider, verify, then make signed delivery available.

Every worker must use durable Appwrite jobs, transactional claim/lease ownership, expired-lease reclamation, bounded retries/backoff, append-only audit records, and an independent kill switch. No browser or node runs these jobs.

## Current code status
- Pure evaluator: implemented in `internal/freshstorage/retention_policy.go`; only `post_media` and `story_media` scopes are eligible. Direct messages, avatars and profile media are rejected by the evaluator.
- Evaluator tests: implemented in `internal/freshstorage/retention_policy_test.go`, including explicit profile-media and direct-message exclusion.
- Existing upload reservation expiry remains separate and must not be reused for ready media.
- Concrete Appwrite job repository, provider archive adapter, story/reference integration, replica-aware purge, restore API, and executable scheduled workers remain pending.
- Retention is disabled and dry-run by default. No destructive operation is authorized by this commit.
