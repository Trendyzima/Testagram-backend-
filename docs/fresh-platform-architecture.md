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

## Storage target: 100 GB logical capacity
Target is 100,000,000,000 bytes (100 GB decimal) of usable logical media capacity. This is a target, not a claim that free plans already provide 100 GB.
- Backblaze B2: primary durable object origin.
- Cloudflare R2: additional object provider and optional independently verified replica/overflow.
- Cloudflare CDN/WAF/DNS: edge delivery/security. CDN cache is not durable storage and is not counted toward capacity.
- Android Node B: optional removable-SD storage-only node. Never an identity/database host or compute worker. Do not count it as durable usable capacity until authenticated placement, checksum verification, independent backup and physical failure tests pass.
- Phone internal storage is never a fallback.

Charge a logical asset once; track physical replicas separately. Reserve quota before upload, validate provider-observed size and SHA-256, and mark media ready only after durability policy passes. ETag is not SHA-256. Never delete until ownership and required replicas are verified.

Safe initial rollout: B2 primary, R2 explicitly configured second provider. Start with one verified durable copy and no automatic failover. Enable dual-copy placement only after restore/reconciliation tests. SD nodes are opt-in intermittent replicas/cache, never the only copy.

Use opaque server-generated object keys. Keep buckets private and credentials server-side. Separate dev/staging/prod credentials. Public cache keys must be immutable/versioned; private media bypasses public shared cache and uses short-lived authorized access.

## Rollout gates
- Separate Appwrite projects, databases, collections, indexes and server keys for dev/staging/prod.
- Auth signup/login/verification/recovery/logout and Backend JWT validation pass E2E.
- Database CRUD, uniqueness, indexes, permissions and failure handling pass.
- Fresh app has no production Supabase dependency or required Supabase environment variables.
- B2/R2 upload/read/head/delete/SHA-256 integration tests pass with test buckets.
- 100 GB quota reservations, races, cancellation, expiry, deletion and reconciliation pass.
- Android Node B physical SD-card tests pass; internal storage rejected; card removal/replacement safe.
- Database and object backups restore successfully.
- Cloudflare public/private caching, origin protection, TLS and access controls pass.
- Load, security, observability and disaster recovery pass before production.

No Supabase migration, no legacy import/linking, no automatic federation activation, and no production-readiness claim based only on compilation.
