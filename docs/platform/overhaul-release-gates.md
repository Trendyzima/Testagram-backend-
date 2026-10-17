# Testagram backend platform overhaul

This plan is a release-gate document, not a claim that a capability is implemented.
Keep production traffic unchanged until each gate has executable evidence.

## Non-negotiable architecture boundaries

- Keep PostgreSQL, Valkey, MinIO, Prometheus, and the platform/control-plane APIs on private networks.
- Never expose internal platform tokens, database credentials, or service-role credentials to browser/mobile clients.
- Keep the canonical user/authentication/RLS contract explicit. Local PostgreSQL does not replace Supabase Auth, schema, policies, or Edge Functions by merely being present in Compose.
- Use backward-compatible migrations; back up and verify existing volumes before applying migrations.
- Do not enable public traffic or DNS changes as part of feature implementation.

## Gate 0 — Establish repository and CI truth

- Verify PR #24's merged status and merge commit (closed is not synonymous with merged).
- Record required checks and their final conclusions for the exact merge SHA.
- Run Go formatting, unit tests, race tests, vet/build, Compose config validation, Caddy validation, and fresh-database migration smoke tests.
- Record test output and failures; never infer green from a closed PR.

## Gate 1 — Durable queue contract

### Delivery semantics
- State at-least-once delivery explicitly; never promise exactly-once side effects.
- Use stable application idempotency keys, independent of Valkey stream IDs.
- Acknowledge only after the durable side effect commits.
- Reclaim idle pending messages with bounded leases and avoid reclaiming valid long-running jobs.
- Use bounded retries with exponential backoff and jitter; classify permanent, transient, cancellation, and infrastructure errors separately.
- Append dead-letter records before acknowledging the source; make dead-letter writes and replays idempotent.
- Do not trim unprocessed streams. Define retention/archival based on acknowledged work and recovery requirements.

### Required fault-injection tests
1. Message enqueued before consumer-group creation is processed.
2. Worker killed after claim; another consumer reclaims the pending job.
3. Worker killed after side effect but before acknowledgement; duplicate delivery does not duplicate the side effect.
4. Valkey restart during enqueue, processing, acknowledgement, and reclaim.
5. Dead-letter append failure leaves source recoverable.
6. Poison payload reaches bounded dead-letter handling without an infinite retry loop.
7. Shutdown cancels bounded I/O and preserves unfinished work.
8. Multiple consumers reclaim concurrently without losing or permanently stranding work.

### Required observability
Expose queue depth, oldest pending age, pending count, reclaimed count, retry count, dead-letter count, processing latency, handler errors, and settlement failures. Avoid labels containing user IDs, job payloads, tokens, or other high-cardinality/secrets.

## Gate 2 — Media service integration

- Authenticate upload initiation and enforce ownership, quota, size, content-type, and allowed media policies server-side.
- Generate opaque object keys server-side; never trust client-supplied storage paths.
- Support resumable uploads with expiry, checksum validation, completion verification, and orphan cleanup.
- Store media metadata and processing state durably before associating media with a post.
- Keep private objects private; issue short-lived signed URLs only after authorization.
- Validate image dimensions and decode limits; isolate video probing/transcoding in bounded workers.
- Make thumbnail/transcode jobs idempotent and record explicit pending/ready/failed states.
- Use versioned cache keys or controlled purging for replacement/deletion. Never publicly cache authenticated responses.
- Test authorization bypass, oversized uploads, MIME spoofing, interrupted uploads, expired signatures, object deletion, and processing retries.

## Gate 3 — Application-level queues

Integrate actual producers and consumers for federation ingestion, feed refresh, media processing, email, notifications, and scheduled work. Each integration needs:
- a stable job schema and version;
- idempotency key and duplicate policy;
- bounded payload and timeouts;
- retry classification and dead-letter procedure;
- metrics and operator-visible status;
- integration tests proving the user-facing application path works.

Do not claim queue infrastructure powers a feature until its real application call sites use it.

## Gate 4 — Search and federation

- Audit existing schemas and contracts before adding indexes or replacing search paths.
- Define index source-of-truth, upsert/delete semantics, deduplication, bounded pagination, query timeouts, and re-index/reconciliation jobs.
- Ensure ingestion failure cannot silently drop posts; track lag and retry terminal failures.
- Preserve federation signature verification, authorization, and instance/account blocks.
- Test duplicate delivery, out-of-order events, deletes, edits, and cold-start re-indexing.

## Gate 5 — Email and notifications

- Persist delivery intents before acknowledging jobs.
- Use provider adapters and a local development sink; never make an unverified external email service a production dependency.
- Record attempt history, next retry, provider message ID, delivery/bounce/complaint status, and suppression state.
- Verify webhook authenticity, replay protection, and idempotent event processing.
- Do not retry permanent bounces indefinitely; protect against notification storms and duplicate sends.

## Gate 6 — Edge, cache, and rate limits

- Specify public/private cache policy by route and content class.
- Never cache authenticated responses in shared cache unless an explicit reviewed design proves safe.
- Bound request bodies, concurrency, timeouts, and per-identity/IP quotas.
- Define safe purge authorization, audit records, and failure behavior.
- Test cache poisoning, origin bypass, stale deleted media, rate-limit backend outage, and private-network access.

## Gate 7 — AI gateway

- Add provider adapters behind a server-only interface; support a local-model adapter without making it mandatory.
- Apply per-user and global quotas, request/output limits, timeouts, cancellation, concurrency bounds, and budget ceilings.
- Redact secrets and sensitive prompts from logs; make retention explicit.
- Provide deterministic mock-provider tests and fail closed when a configured hard budget is exhausted.
- Never expose provider API keys to the client.

## Gate 8 — Developer and operator platform

- Keep control-plane routes private and require strong operator authentication.
- Publish versioned OpenAPI contracts and generated/validated API docs.
- Audit privileged actions; show deploy state, worker health, queue lag, media failures, capacity, and backup age.
- Require health/readiness gates and rollback instructions for deployments.
- Do not enable registrar/DNS writes, automatic secret resolution, or public administration until separately reviewed and tested.

## Gate 9 — Recovery and release

- Produce encrypted off-host PostgreSQL backups and object-store backups/replication.
- Demonstrate restore into an isolated environment and validate row/object counts and application behavior.
- Treat Valkey queue persistence and recovery as a separate test; do not assume cache backup semantics apply to jobs.
- Test power-loss/disk-full/network-partition scenarios and alert on backup age, disk pressure, queue lag, and service saturation.
- Run representative load tests and report the measured workload and bottleneck. Do not claim capacity from account count or a smoke test.
- Promote only when required CI, security, migration, integration, recovery, and rollback gates have recorded evidence.

## Definition of done

A capability is complete only when its implementation is used by the application, tests cover success and failure paths, metrics and operator procedures exist, secrets and network boundaries are verified, migrations are safe for existing data, and CI passes on the exact proposed commit. This document alone satisfies none of those implementation gates.
