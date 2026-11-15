# Authentication and hybrid storage production hardening

**Status:** implementation/release checklist. This document does not change the production identity provider, storage provider, DNS, secrets, database or VPS deployment.

## Production invariants

- Appwrite remains the active production credential/session authority until the native PostgreSQL path has passed every gate in this document and an explicitly approved cutover.
- PostgreSQL on the InstaBox VPS remains the canonical authority for Testagram account UUIDs, identity links, profiles, social graph, posts, visibility, moderation, media metadata, quotas, replica ledger and outboxes.
- Existing account UUIDs, Appwrite account IDs and ownership references are immutable migration inputs. Never regenerate IDs, infer identity links from email equality, or delete legacy mappings during shadow migration.
- Cloudflare DNS remains the public edge. Testagram Go CDN owns public media byte delivery. Cloudflare R2 and Backblaze B2 are private object stores, not competing public media origins.
- User media durability target is three independently verified copies: private VPS MinIO, private Cloudflare R2 and private Backblaze B2. CDN cache is disposable and never counts as a replica.
- Render is not a runtime, storage or deployment target.

## Authentication hardening

### Current production path

Flutter obtains an Appwrite session and sends its session JWT to Backend. Backend validates the session with Appwrite and resolves the explicit provider-to-canonical UUID mapping in PostgreSQL. Unknown/unlinked, unverified, inactive or unprovisioned identities fail closed.

Do not point Flutter at legacy local-password endpoints merely because those handlers compile. Do not enable a native-auth feature flag or remove Appwrite configuration as part of this PR.

### Native authentication completion gates

1. Inventory every public, staff/admin, internal-service, federation and media route. Record required principal, role/scope, canonical UUID resolution and authorization checks.
2. Reconcile Appwrite source account IDs to existing canonical Testagram UUIDs. Produce a signed-off conflict report; ambiguous mappings are quarantined, never auto-linked by email.
3. Preserve verified-email provenance and existing account status. Never mark an address verified based only on a matching string.
4. Keep native credential/session records additive. Do not fabricate password hashes or claim provider password export. Accounts without portable credentials use an explicit reset/re-enrolment path.
5. Complete access-token issuer/audience validation, short expiry, refresh-token rotation and reuse detection, revocation, logout-all, credential-version invalidation, recovery-token single use, rate limits, generic recovery responses, audit records and staff MFA/step-up requirements.
6. Use a dedicated signing key with rotation/key-version strategy; do not reuse Appwrite, CDN, storage or service-to-service secrets.
7. Keep the Appwrite path as production default during shadow verification. Compare identity resolution outcomes without allowing native shadow results to grant access.
8. Test Flutter login, app restart, session expiry, refresh, logout, account recovery, email verification, device replacement and revoked/inactive users.
9. Require migration count reconciliation, ownership foreign-key checks, security review, backup/restore proof, rollback rehearsal and explicit operator approval before cutover.
10. Retain the Appwrite validation and mapping rollback path for the agreed observation window. Do not delete legacy identity links until a separate approved retirement PR.

### Security acceptance

- Password hashes use a memory-hard password hashing algorithm where supported by the deployed Go implementation; if PBKDF2 remains for compatibility, document its work factor and plan a versioned rehash-on-login migration.
- Passwords, JWTs, refresh tokens, reset/verification tokens and provider secrets never appear in logs, analytics, URLs or crash reports.
- Authentication and recovery endpoints have per-account and per-IP abuse controls, generic error behavior, TLS, strict origin policy and no-store responses.
- Internal service credentials have separate audiences/scopes and are never accepted as user bearer tokens.
- All authorization is enforced server-side against canonical PostgreSQL state; client role claims are never authoritative.

## Storage architecture

### Ownership

- Backend: upload intent, canonical owner UUID, namespace, visibility, quotas, content policy, SHA-256, asset state, deletion tombstones and public/private authorization.
- Data Centre: VPS MinIO, B2 and R2 provider operations, replication jobs, leases, repair, reconciliation, backups and restore evidence.
- Go CDN: byte delivery, authorized origin selection, range/HLS streaming and disposable caching.
- Cloudflare: authoritative DNS/domain security and private R2 object storage. Do not attach a competing Worker route to the active Go CDN media hostname.
- PostgreSQL: canonical media metadata and durable replica ledger. Operational media-service database may hold job/lease/receipt state but cannot become a second product-media authority.

### Three-copy state machine

An asset moves through upload_pending -> verifying -> replicating -> ready only after all configured destinations independently pass:
- exact object-key and owner/namespace binding;
- HEAD size and content-type comparison;
- full streamed SHA-256 read-back comparison (ETag is not SHA-256);
- durable provider receipt recorded in the replica ledger.

The three destinations are:
1. InstaBox VPS private MinIO;
2. Cloudflare R2 private bucket;
3. Backblaze B2 private bucket.

If any provider is unavailable or checksum mismatches, keep the asset replicating/degraded; never claim three-copy readiness. Use leased, idempotent jobs, bounded exponential retry, dead-letter inspection and periodic full read-back reconciliation. Restore from a surviving verified copy only after matching the canonical SHA-256 and byte length. Do not delete the final good copy before replacement verification.

### Upload and delivery security

- Client receives only short-lived object-scoped upload capability; never receives S3 master keys.
- Upload completion must verify provider-observed size, MIME and client-declared SHA-256 against a streamed server-side digest.
- Public and private namespaces use separate buckets or equivalent enforceable policies.
- Private reads require a short-lived Backend assertion bound to viewer, canonical object key, owner, issuer, audience and expiry. CDN forwards it to Backend origin and does not cache private bytes.
- Public reads are allowed only after Backend confirms canonical visibility=public and status=ready. Object key prefixes are not visibility authority.
- CDN must not direct-fallback to R2/B2 or MinIO unless the provider adapter independently enforces the same Backend authorization contract.
- Deletion is a durable tombstone/outbox operation; all replicas are deleted idempotently and receipts recorded. Cache purge is not object deletion.

## Rollout order

1. Staging-only provider credential validation and bucket-policy audit.
2. VPS MinIO, R2 and B2 isolated upload/read-back/checksum/delete tests.
3. Enable three-copy replication for a synthetic staging namespace; verify all receipts and repair after deliberately removing one replica.
4. Exercise private/public CDN authorization, range requests, HLS and fail-closed origin outage behavior.
5. Exercise Flutter upload, completion, playback, delete and retry on low-memory mobile devices.
6. Reconcile existing media inventory and backfill replicas in bounded batches; do not flip legacy URLs until every object has verified placement.
7. Canary new uploads, monitor degraded counts and replication latency, then request explicit production approval.
8. Keep rollback to the previous media path until restore, delete and authorization tests pass.

## Release checklist

- [ ] No production authentication provider or session behavior changed.
- [ ] Existing account IDs and ownership references reconcile exactly.
- [ ] Native auth passes route-by-route parity and rollback tests.
- [ ] VPS MinIO, R2 and B2 each pass independent read-back SHA-256.
- [ ] PostgreSQL replica ledger matches provider receipts.
- [ ] Failed replica repair and restore drills pass.
- [ ] Private media is never shared-cacheable.
- [ ] Public media requires Backend visibility classification.
- [ ] CDN does not expose provider URLs or credentials.
- [ ] No Render dependency or deployment target is introduced.
- [ ] VPS staging evidence, monitoring and rollback are attached to the release PR.
