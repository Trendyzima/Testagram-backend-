# Testagram hybrid migration readiness

## Target ownership model

- **Appwrite Auth:** primary authentication provider after staged account linking. Appwrite session JWTs are verified by the Go API against Appwrite's authenticated `/account` endpoint; the backend does not trust decoded client claims.
- **Independent PostgreSQL:** canonical Testagram UUIDs, profiles, posts, relationships, federation state, media metadata, quotas, storage-node inventory, TV health records and transactional outbox.
- **Go backend:** only public application API and trusted coordinator for database writes, federation, storage placement and node control.
- **Appwrite Realtime:** derived application-facing projections/events. It is not a second source of truth.
- **S3-compatible object storage:** Backblaze B2 as cloud object origin, with Cloudflare as the delivery/cache layer; PC and Android nodes are additional providers only after their agent protocols are deployed and tested.

## Current implementation baseline

The backend already contains an S3-compatible SigV4 provider, virtual-storage policy manager, SHA-256 verification, a PostgreSQL 100 GiB logical pool migration, upload reservation accounting, node/replica inventory tables, independent local-auth code, and replication/outbox foundations. These are useful foundations, but their presence does not mean the end-to-end production gates have passed.

This branch adds:

- An Appwrite session verifier that calls Appwrite `/v1/account` with `X-Appwrite-Project` and `X-Appwrite-JWT`.
- Explicit `TESTAGRAM_AUTH_PROVIDER=appwrite` selection. It does not silently fall back to Supabase if Appwrite is unavailable.
- A PostgreSQL mapping from Appwrite user IDs to stable Testagram UUIDs, with an explicit `legacy_user_id` link slot.
- Safe defaults that retain the current Supabase auth path until the client and existing-account migration are ready.

## Acceptance checklist and honest status

| Gate | Current status | Remaining acceptance evidence |
|---|---|---|
| Appwrite authentication | **Backend adapter implemented; not production-enabled** | Staging Appwrite project tests, session expiry/revocation behavior, client SDK integration, and account-link migration for existing users |
| PostgreSQL consistency | **Foundation exists; partial** | Apply the complete ordered schema to a clean staging DB, test constraints/transactions/outbox replay, and verify data reconciliation |
| 100 GiB global quota | **Schema foundation exists; partial** | Concurrency tests against the final Go upload routes, pool usage reconciliation, and actual provider capacity proof |
| Backblaze direct uploads | **Generic S3 provider exists; not B2-certified** | B2-specific signed PUT/HEAD/GET/DELETE, multipart behavior if enabled, SHA-256 verification and CDN origin tests |
| Computer nodes | **Inventory schema only** | Authenticated agent enrollment, signed heartbeat, safe folder allowlist, capacity reports, placement, offline recovery and replica verification |
| Android Node B | **Separate client foundation exists; not integrated end-to-end** | Removable-SD-only enforcement, card replacement, no internal-storage fallback, heartbeat, capacity, upload and recovery tests |
| Federation | **Existing implementation remains Supabase-coupled** | Move WebFinger, NodeInfo, actor/inbox/outbox and delivery workers to Go/PostgreSQL; preserve actor IDs and signing keys; pass live federation probes |
| TV health | **Existing implementation remains Supabase-coupled** | Add Go/PostgreSQL persistence and move TV clients/workers; prove write/read/restart durability |
| Security | **Partial** | Appwrite identity mapping review, authorization matrix, signed-upload replay tests, private media isolation, node enrollment threat model and secret scanning |
| Recovery | **Existing backup tooling; not certified for this migration** | Encrypted backup, isolated restore drill, database reconciliation, object restore from replica and documented RPO/RTO |

## Existing-account migration policy

Appwrite user IDs are not assumed to equal the existing Supabase UUID. The mapping table provides a stable canonical UUID. A legacy account must be linked only after a verified ownership process (for example, an authenticated legacy session plus an Appwrite session, or a verified recovery flow). Email equality by itself is not sufficient proof. The linking operation must be transactional, unique on both identities, auditable, and idempotent. Existing actor IDs and federation signing keys remain unchanged.

Do not bulk-create Appwrite accounts from email addresses without a verified invitation/reset workflow. Do not copy password hashes from Supabase Auth; normal Auth APIs do not expose them.

## Storage rules

- The logical global cap is 107,374,182,400 bytes (100 GiB); it is not a claim that 100 GiB of free physical storage has been provisioned.
- Logical quota is charged once per verified asset. Every physical replica consumes provider/node capacity separately.
- Default user quota remains 2 GiB and is independently configurable; never set it to 100 GiB to represent the global pool.
- A node is eligible only when enabled, authenticated, online within the heartbeat freshness window, and above its free-space floor.
- An offline node's objects are not considered available. A replica is protective only after destination checksum and size verification.
- Signed URLs are short-lived and provider-scoped. Provider credentials never reach clients. Private media must not be publicly cached.

## Rollout and rollback

1. Keep the current Supabase deployment and exports as a recovery source.
2. Provision staging Appwrite and independent PostgreSQL; apply migrations through the reviewed migration runner.
3. Backfill and reconcile canonical users/data; test the account-link flow before any client switch.
4. Enable Appwrite auth only in staging using `TESTAGRAM_AUTH_PROVIDER=appwrite`; Appwrite failure returns an auth-unavailable response and never falls back to Supabase.
5. Move application capabilities by bounded domains, with the Go API as the only write path and transactional outbox for derived Appwrite Realtime events.
6. Move media only after B2 and node placement tests pass; verify every object by SHA-256 before metadata cutover.
7. Move federation and TV health only after contract parity and persistence tests pass.
8. Cut over production after all checklist gates and live probes pass. Keep Supabase read-only/recoverable until reconciliation and rollback windows expire.

Rollback is a routing/configuration decision only while dual-read/reconciliation remains safe. Never attempt bidirectional writes to both old and new databases without a specific conflict-resolution protocol. Never delete the source database or old media until the restore and reconciliation sign-off is recorded.

## Current branch boundary

This change is a backend migration foundation, not the complete Supabase exit. The frontend still has broad direct Supabase Auth, PostgREST, Realtime and Edge Function dependencies. The capability gateway, admin console, media metadata bridge, federation functions and TV health functions remain migration work. Production auth provider defaults to the existing Supabase path until a separate client migration and staging validation are complete.
