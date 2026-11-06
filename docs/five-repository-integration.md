# Testagram five-repository integration contract

## Single frontend rule

There is exactly one product frontend: Trendyzima/XClone-9b4dz1-9b4dz1-8936-20260809tr.
It is the only browser/mobile UI. It calls the public Testagram API origin; it must never call the algorithm ranker, federation worker, PostgreSQL, Redis, storage nodes, or VPS control-plane directly.

Recommended browser configuration:
- VITE_TESTAGRAM_API_URL=https://api.testagram.site
- VITE_TESTAGRAM_UNIFIED_FEED_ENABLED=true only after backend migrations and the API/ranker services have passed staging acceptance.
- VITE_TESTAGRAM_AUTH_PROVIDER must match the backend's configured auth provider. Never mix Appwrite and Supabase sessions.

No service credential, internal bearer token, database URL, storage key, or control-plane token may use a VITE_ variable.

## Runtime ownership

| Repository | Runtime responsibility | Authority |
|---|---|---|
| XClone | The single UI and client-side state | No server-side authority |
| Testagram Backend | Public API, auth validation, local accounts/posts/social graph, authorization, feed orchestration and media lifecycle | Canonical local product data and permissions |
| The Algorithm | Internal ranking and recommendation computation | Derived scores only; no user/post ownership |
| Federated Content | ActivityPub actor/protocol state, remote actors and delivery | Federation protocol state only; never canonical local account data |
| Testagram Data Centre | VPS assembly, TLS/edge routing, service deployment, node/storage operations and backups | Infrastructure and physical placement only |

The data-centre repository is the deployment/control-plane layer. The backend repository owns application API and media ownership. Do not create a second frontend or copy backend domain logic into the data-centre repository.

## Feed path implemented in this integration branch

1. XClone calls GET /api/feed/home on the single public API origin with its user's bearer session.
2. Backend validates the session using its configured auth provider.
3. Backend queries canonical PostgreSQL testagram_local posts/accounts/follows/blocks/mutes and applies visibility and protected-account rules before candidate construction.
4. Backend sends only a bounded, already-authorized candidate batch to the-algorithm over the private Docker network using ALGORITHM_INTERNAL_TOKEN.
5. Ranker returns candidate IDs, scores and reasons. Backend hydrates only IDs from its authorized candidate set.
6. If ranker is unavailable or times out, Backend returns the same authorized candidate set in chronological order. Feed availability must not depend on ranker availability.
7. Backend cursor is based on the chronological candidate window, not rank order.

The endpoint currently ranks only rows present in PostgreSQL testagram_local.posts. The local-product write path remains feature-gated by default, and this integration does not migrate legacy Supabase posts. Do not enable the frontend flag until existing posts have been migrated or canonical local writes are enabled and verified. Remote federation items are not yet ingested into that candidate store by the new federation service; XClone's existing federated timeline adapter remains a transitional path until the federation inbox/outbox and durable event consumer are implemented and staged.

## Media path

XClone requests upload/download authorization only from Backend. Backend validates identity, owner, visibility, quotas and object lifecycle, then uses the configured S3-compatible provider or current media path. Data Centre provisions and protects the host, object storage mounts, network, backup and Android Node B infrastructure. Do not expose storage-node APIs or provider credentials to XClone.

Keep VIRTUAL_STORAGE_ENABLED=false until the documented provider/CDN path, checksums, durable metadata, quotas, and live object-store lifecycle gates pass. Android Node B is optional removable-SD-only capacity, never the sole durable copy and never a compute worker.

## Federation path and current limitation

The federation container can be deployed privately by Data Centre. Its current implementation only publishes one configured service actor and an empty outbox; inbox processing returns HTTP 501 until HTTP Signature verification and durable storage are implemented. Therefore it must not be advertised as production federation or receive public federation traffic yet. The next release must implement:
- dynamic actor resolution against Backend-owned account identities;
- verified inbound HTTP signatures and digest, actor/key ownership binding, replay protection and deduplication;
- durable PostgreSQL inbox/outbox and queue-backed delivery;
- SSRF-safe remote fetch and delivery, host rate limits, retry/backoff and dead-letter handling;
- a versioned Backend transactional-outbox consumer for post/account lifecycle events.

## Environment contract

| Variable | Consumer | Meaning |
|---|---|---|
| ALGORITHM_URL | Backend | Private ranker origin, e.g. http://algorithm:8092 |
| ALGORITHM_INTERNAL_TOKEN | Backend + ranker | Shared secret, minimum 32 characters; rotate together |
| DATABASE_URL | Backend | Canonical PostgreSQL connection |
| TESTAGRAM_AUTH_PROVIDER | Backend | Exactly one selected auth provider |
| SUPABASE_URL, SUPABASE_PUBLISHABLE_KEY | Backend | Transitional Supabase session validation only |
| APPWRITE_ENDPOINT, APPWRITE_PROJECT_ID | Backend | Appwrite session validation when selected |
| MEDIA_S3_* | Backend | Server-side object-store credentials and endpoint |
| TESTAGRAM_API_DOMAIN | Data Centre edge | Public API hostname routed to Backend |

All service-to-service calls are private-network only, bounded by request timeouts and body limits. Use TLS at the public edge. Never publish PostgreSQL, Redis, ranker, federation internals or control-plane ports directly.

## Release gates

- Run all five repository CI suites and the XClone typecheck/build on the exact commits.
- Apply and verify backend migrations before enabling the new feed route in XClone.
- Deploy ranker and Backend on one private network; test authorization, block/mute/protected-account filtering, ranking order, timeout fallback and cursor pagination.
- Confirm one API origin serves XClone's public API calls; no browser-visible service token or private service hostname.
- Keep federation private until its protocol security and durable inbox/outbox gates pass.
- Test media upload, read, delete and backup/restore against the actual provider.
- Verify API DNS/TLS, CORS, logs, health/readiness and rollback on staging before production routing.

A green build is not proof of a working distributed deployment. Production cutover requires live staging tests with the real database, identity provider, object store, edge and browser.
