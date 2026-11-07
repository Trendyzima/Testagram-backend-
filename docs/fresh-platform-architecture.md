# Fresh Testagram platform — clean-room baseline

## Non-negotiable boundary
New platform, not a migration. Existing Supabase is legacy-only and must remain untouched: do not connect fresh production code to its credentials, read/copy its rows or objects, run migrations against it, or delete/alter data. No legacy-account linking or ID reconciliation.

## Primary application stack
- Appwrite Auth is the sole identity/session authority for new accounts.
- Appwrite Database is the canonical application data store.
- Go Backend is the trusted API/policy boundary; Appwrite server keys never reach browsers or phones.
- XClone is the only first-party frontend.
- Algorithm ranks only Backend-authorized candidate batches and never queries Appwrite.
- Data Centre owns deployment, node control plane, storage placement and operations.
- Federation remains disabled until signature verification, inbox/outbox safety, replay controls and security review pass.

## Appwrite project and data model
Create a separate Appwrite project/database for each environment. New user IDs are Appwrite $id values. Never reuse Supabase IDs. Keep document permissions private by default; the Backend service key performs application reads/writes. Clients authenticate with Appwrite sessions and call the Backend for application data.

Initial collections: profiles; account_settings; follows; blocks; posts; post_media; likes; comments; reposts; bookmarks; notifications; conversations; conversation_members; messages; media_objects; storage_reservations; storage_nodes; storage_replicas. Add indexes for every lookup and uniqueness path. Enforce privacy, block, membership and ownership rules in the Backend. Do not make messages, notifications, user records or storage metadata public.

## Authentication contract
1. Client creates an Appwrite session.
2. Client creates a short-lived Appwrite JWT and sends it only to the configured HTTPS Backend.
3. Backend validates the JWT by calling the configured Appwrite Account endpoint; never trust decoded claims alone.
4. Backend uses Appwrite $id as the new Testagram identity. No Supabase lookup or legacy linking.
5. Appwrite handles verification, recovery and session revocation; Backend authorizes every application action against Appwrite Database.

## Storage target: 150 GB logical capacity
Target is 150,000,000,000 bytes (150 GB decimal) of usable logical media capacity. This is a target, not a claim that free plans or enrolled devices already provide 150 GB. Each logical asset is counted once; every physical replica is tracked and budgeted separately. A two-copy 150 GB pool may require up to 300 GB of physical capacity when both copies cover the entire dataset.

- Backblaze B2: preferred durable object origin.
- Cloudflare R2: additional durable object provider for verified replicas and controlled overflow. Do not count CDN cache as storage.
- Cloudflare CDN/WAF/DNS: edge delivery/security only; CDN cache is never counted as durable capacity.
- Android Node B: removable-SD-only storage node. It never uses internal phone storage and never performs compute. Its local media endpoint is currently loopback-only; remote object transport and replica placement are not complete, so SD capacity is not yet counted toward usable platform capacity.
- Computer nodes: independently opt into compute, storage, or both. Compute-only nodes contribute CPU/RAM but no media capacity. Storage-enabled Linux computers require a dedicated mounted filesystem and explicit quota, separate from OS and compute paths. Windows/macOS adapters and remote media object transport are not complete.
- Node capacity is counted only after authenticated enrollment, fresh heartbeats, quota reservation, successful object write/read-back, SHA-256 verification and a tested recovery path. The current node control plane reports capacity as reported-only, not verified usable capacity.

The fresh Go media lifecycle now reserves platform and per-user quota transactionally in Appwrite (KiB units, rounded up), issues idempotent B2 upload reservations, validates B2 object size/MIME/SHA-256, streams a bounded copy to R2 and independently verifies the R2 read-back checksum. It records B2/R2 replica documents and marks an object protected only after both cloud replicas pass. Abandoned reservations have a lease-protected expiry sweeper; operator-triggered repair can restore one cloud replica from the other. ETag is not SHA-256. Never delete until ownership and required replicas are verified.

Safe initial rollout: B2 is the preferred cloud origin and R2 is the second cloud provider. Keep automatic failover disabled until restore/reconciliation tests pass. Android SD and computer nodes are future optional extra replicas; they must never become the only copy. Keep a durable cloud copy for every asset and require two independently verified cloud copies before marking it fully protected.

Use opaque server-generated object keys. Keep buckets private and credentials server-side. Separate dev/staging/prod credentials. Public cache keys must be immutable/versioned; private media bypasses public shared cache and uses short-lived authorized access.

## Rollout gates
- Separate Appwrite projects, databases, collections, indexes and server keys for dev/staging/prod.
- Auth signup/login/verification/recovery/logout and Backend JWT validation pass E2E.
- Database CRUD, uniqueness, indexes, permissions and failure handling pass.
- Fresh frontend mode selects Appwrite, removes legacy identity linking, disables Supabase clients to loopback and fails closed for unported social endpoints; no fresh request may reach the legacy Supabase/API host.
- B2/R2 upload/read/head/delete/SHA-256 tests pass against isolated staging buckets (local mocked-provider tests alone are insufficient).
- 150 GB quota reservations, races, cancellation, expiry, deletion and reconciliation pass.
- Android Node B physical SD-card tests pass; internal storage rejected; card removal/replacement and authenticated remote transfer are safe. Computer-node compute-only, storage-only and dual-purpose enrollment tests pass; storage quotas and disjoint media/compute paths are enforced. These gates remain open.
- Database and object backups restore successfully.
- Cloudflare private-media Worker is deployed with the correct R2 binding, HMAC secret, exact frontend origins, TLS and no-store policy. Public-media caching requires a separate explicit authorization/cache policy.
- Load, security, observability and disaster recovery pass before production.

No Supabase migration, no legacy import/linking, no automatic federation activation, and no production-readiness claim based only on compilation.

## Current fresh frontend boundary

The fresh environment example sets VITE_TESTAGRAM_FRESH_MODE=true, VITE_TESTAGRAM_AUTH_PROVIDER=appwrite, and points both API-origin variables at the fresh Go API. Fresh builds disable legacy Supabase clients rather than connecting to the old project. The new browser media upload/download service exists, but it is not yet wired into post, message, story or profile composers. Social/feed endpoints deliberately fail closed in fresh mode until their Appwrite-backed implementations are ready; a successful frontend build does not mean those screens are migrated.
