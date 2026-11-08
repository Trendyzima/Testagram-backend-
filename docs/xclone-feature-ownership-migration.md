# XClone feature ownership and backend migration plan

## Objective

Make XClone a client-only application and move every durable feature, business rule, permission check, workflow and integration into the five-repository Testagram service architecture. This document is the Backend-side ownership ledger for the migration.

## Service boundaries

- **Testagram Backend (this repository):** public authenticated API; Appwrite identity/session verification; canonical local profiles, social graph, posts, visibility, moderation orchestration, feed orchestration, messaging, calls, Spaces, wallets, payments, ads, communities, marketplace, notifications, settings and application policy.
- **Federated Content:** ActivityPub protocol state, remote actor/key resolution, signed inbox, protocol outbox and remote delivery. It must resolve local actors and permissions through Backend internal contracts.
- **The Algorithm:** ranking and recommendation scoring only. It does not own canonical posts, user identity, visibility or authorization. Backend rechecks visibility after ranking.
- **Testagram Data Centre:** private deployment, storage durability, upload lifecycle infrastructure, B2/R2 replication, CDN/media workers, realtime transport infrastructure, storage nodes, backup and recovery.
- **XClone:** UI, navigation, forms, local ephemeral state, playback and public API adapters only.

## Migration ledger

| Feature family | Backend ownership / target API family | Cross-service dependency | Acceptance evidence |
| --- | --- | --- | --- |
| Appwrite auth, identity, account lifecycle | `/v1/auth/**`, `/v1/me/**` | Appwrite | JWT verification, account/profile lifecycle and session restoration tests |
| Profiles, settings, legal, privacy, sessions | `/v1/me/**`, `/v1/users/**` | Appwrite | Owner/other-user authorization and privacy tests |
| Posts, replies, quotes, likes, reposts, bookmarks, polls | `/v1/posts/**`, `/v1/social/**` | Neon | Transactional integration tests and idempotent mutation tests |
| Follows, blocks, mutes, lists, communities | `/v1/graph/**`, `/v1/communities/**` | Neon | Visibility, membership and abuse-policy tests |
| Feeds, search, discovery, trends | `/v1/feed/**`, `/v1/search/**` | Algorithm | Candidate hydration, visibility recheck, cursor and fallback tests |
| Direct messages and conversations | `/v1/messages/**`, `/v1/conversations/**` | Data Centre realtime | Durable send/receive, ordering, receipts, attachment and authorization tests |
| Voice/video calls | `/v1/calls/**` | Data Centre realtime/media | Authenticated call creation, join/leave, signalling authorization and reconnect tests |
| Audio Spaces/live audio | `/v1/spaces/**` | Data Centre realtime/media | Host/speaker/listener roles, moderation, audience limits and reconnect tests |
| Wallets, ledger, credits, transfers, savings | `/v1/wallet/**`, `/v1/ledger/**` | Neon | Double-entry invariants, idempotency, concurrency and reconciliation tests |
| M-Pesa, PayPal, Pesapal, subscriptions, payouts | `/v1/payments/**`, `/v1/payouts/**` | Provider adapters and workers | Signature verification, callback replay, settlement and refund tests |
| Ads, campaigns, boosts, creator monetization | `/v1/ads/**`, `/v1/monetization/**` | Neon; Algorithm only for ranking signals | Budget enforcement, billing ledger and advertiser isolation tests |
| Notifications and push | `/v1/notifications/**`, `/v1/push/**` | Data Centre delivery workers | Durable enqueue, retries, preferences and deduplication tests |
| Federation controls and local object orchestration | `/v1/federation/**` | Federated Content | Consent, actor mapping and outbox handoff tests |
| Uploads, media metadata, access grants | `/v1/media/**` | Data Centre and CDN | Authorized reservation, completion, signed download and deletion tests |
| Marketplace, orders, delivery, rides | `/v1/marketplace/**`, `/v1/orders/**`, `/v1/rides/**` | Data Centre where needed | Inventory, order, settlement and delivery lifecycle tests |
| TV/IPTV catalog and entitlements | `/v1/tv/**` | Data Centre/CDN | Catalog freshness, health, entitlement and playback authorization tests |
| AI, transcription, help/support, moderation | `/v1/ai/**`, `/v1/moderation/**`, `/v1/support/**` | Private provider adapters | Quotas, input validation, audit and provider-failure tests |

The route families above are target contract namespaces, not a claim that all handlers are already implemented. Existing routes must be reused where compatible; do not create duplicate handlers simply to match the proposed naming.

## Current implementation boundary observed

The fresh API currently registers:
- `GET /v1/me`
- `PUT /v1/me/profile`
- `GET/PUT /v1/me/legal`
- `GET/PUT /v1/me/federation`
- `GET /v1/feed/home` and `GET /v1/feed/public`
- `POST /v1/posts`
- `POST /v1/moderation/check`
- Media upload reservation/completion/download/archive restore when storage is configured
- Private federation readiness, outbox and actor lookup routes when the internal token is configured

The existing compatibility API also has social/feed/media/federation handlers and a capability gateway, but its README explicitly documents that the capability gateway forwards to the legacy Supabase capability RPC. That is not an acceptable final fresh-platform dependency.

The fresh API currently configures Appwrite as identity/profile storage and optional Neon-backed feed/social operations. It fails closed for publishing when moderation is not configured and supports chronological feed fallback unless ranking is required. Production feature enablement must respect those server-side gates.

## Required implementation waves

### Wave 1 — remove the legacy runtime bridge
- Enumerate all XClone calls to Supabase Auth, PostgREST, Storage, Realtime and Edge Functions.
- Replace the Supabase capability gateway with native Backend dispatch for each capability.
- Add explicit route/contract coverage and server-side authorization.
- Ensure fresh mode cannot silently route to Supabase.

### Wave 2 — social core parity
- Complete profile/account lifecycle, posts, replies, quote/repost/reaction/bookmark, follow/block/mute, polls, lists and communities.
- Ensure Neon is canonical for social records and use Appwrite only for identity/profile fields that are intentionally stored there.
- Add migrations and Postgres integration tests.

### Wave 3 — messages, calls and Spaces
- Add durable conversation/message APIs and authorization.
- Add call session and signalling-ticket APIs; never let the client authorize peer membership.
- Add Spaces lifecycle, speaker requests, role transitions, audience limits, moderation and recording metadata.
- Integrate Data Centre realtime transport without making it the authority for product permissions.

### Wave 4 — wallet, payments and monetization
- Port the frontend payment/monetization modules into server packages.
- Use an append-only, double-entry ledger; never accept a client-calculated balance.
- Verify provider callbacks cryptographically, enforce idempotency, reconcile settlement and make refunds/payouts auditable.
- Keep provider credentials and signing secrets server-side.

### Wave 5 — discovery, ads, commerce, TV and support
- Port remaining search/discovery, recommendation, advertising, marketplace, ride, TV, podcast/news and support capabilities.
- Use Algorithm only for ranking/scoring and re-check visibility in Backend.
- Use Data Centre only for infrastructure/media durability and delivery.

### Wave 6 — cutover and retirement
- Move every XClone query/mutation to the public Backend API.
- Run web/mobile/TV/desktop end-to-end tests against deployed services.
- Remove frontend Supabase dependency, environment variables, SDK, legacy Edge Function source and migration material after all behavior has a replacement.
- Remove compatibility gateway code that forwards to Supabase.
- Verify no direct Supabase runtime dependency remains in any of the five repositories.

## Security and correctness requirements

- Appwrite JWT validation on every authenticated public request.
- Explicit authorization for object ownership, visibility, community roles, message participants, call/Space roles and wallet ownership.
- Bounded request bodies, timeouts, pagination, rate limits and structured request IDs.
- Idempotency keys for payments, wallet mutations, posts and retried commands.
- Append-only ledger and reconciliation for money movement.
- Outbox/inbox processing with durable cursors, deduplication and retry/dead-letter behavior.
- Algorithm responses are untrusted ordering hints; Backend hydrates canonical records and rechecks visibility.
- Private Federation/Data Centre/Algorithm credentials never enter XClone.
- No success-shaped fallbacks when a required dependency is unavailable.

## Completion criteria

A feature is migrated only when:
1. Its business logic and durable state live in the owning service.
2. Its public API contract is documented and implemented.
3. Authorization and validation are server-side.
4. Unit and integration tests pass.
5. XClone calls the API adapter and contains no authoritative implementation.
6. An end-to-end test proves the user journey against the deployed service.

A source file being copied or a route being registered is not sufficient evidence of migration.
