# Testagram five-repository API integration architecture

## Decision

Testagram consists of exactly five repositories. The Backend is the centralized API integration layer and canonical application authority. Do not create a sixth API/client repository. Do not let frontend, ranking, federation or infrastructure repositories become alternate owners of identity, social records, authorization or lifecycle decisions.

## Ownership and communication

| Repository | Owns | Talks to |
|---|---|---|
| XClone | Web/mobile UI, local presentation state, client-side upload orchestration | Backend public API only; Appwrite Auth SDK only for identity/session operations |
| Testagram Backend | Canonical identity verification, authorization, profiles/social graph/posts/engagement/notifications, API contracts, orchestration, lifecycle and audit | Appwrite Auth/Database; private Algorithm API; private Data Centre platform/storage APIs; private Federated Content API |
| The Algorithm | Stateless feed ranking/recommendation over bounded candidate batches | Backend only; never Appwrite, object stores, browser or frontend |
| Federated Content | ActivityPub protocol, remote actors, inbox/outbox, signatures and delivery state | Backend only for canonical authorization/content decisions; remote ActivityPub peers |
| Data Centre | VPS/runtime, private service networking, storage providers, CDN/edge, node enrollment/placement and operations | Backend through authenticated infrastructure APIs; object providers and enrolled nodes |

## Authentication boundaries

1. Human user signs in through the fresh Appwrite Auth project. XClone creates a short-lived Appwrite JWT and sends `Authorization: Bearer <JWT>` to the Backend. The Backend validates that JWT against the configured Appwrite project and derives the user ID from the verified account response. Never accept a browser-supplied owner/user ID as authority.
2. Backend-to-service calls use distinct, high-entropy credentials per service, scoped to the minimum allowed operations. Tokens are server-side only, rotated, redacted from logs, and compared in constant time. Do not reuse the user JWT as a service credential.
3. Algorithm currently exposes `POST /v1/feed/rank` with a bearer service token. Only Backend-supplied, already-authorized and already-filtered candidate batches may reach it. The Backend validates that the ranker returns exactly the submitted candidate IDs and re-reads post status, visibility, account settings, follows and blocks after the ranking round trip before returning content.
4. Federated Content's public ActivityPub routes are not a trusted Backend integration API. Add a separate private authenticated Backend-to-Federation API for enqueueing authorized activities and reporting delivery state. Public inbox processing remains unavailable until HTTP Signature verification, replay protection and durable inbox/outbox processing pass readiness gates.
5. Data Centre's private cache/rate-limit/queue/storage/node operations are infrastructure APIs. The Backend is the product-facing policy and authorization layer. Browser clients never receive Data Centre, storage-provider, node or internal queue credentials.

## API conventions

- Public prefix: `/v1`; private service prefix: `/internal/v1` (private network plus service credential).
- JSON request/response; UTF-8; UTC RFC3339 timestamps; byte counts as decimal strings where values may exceed JavaScript safe integers.
- Target response envelope: `{ "ok": true, "data": ..., "request_id": "..." }`. The current fresh API still needs request-ID propagation and structured error codes; do not claim those conventions are fully implemented yet.
- Release gate: generate a request ID, propagate it across service calls and include it in audit/structured logs. Never log JWTs, API keys, passwords, signed URLs or private message bodies.
- Mutations accept `Idempotency-Key`; persist its scope, request hash and result atomically. Reuse with a different payload returns 409.
- Enforce strict request schemas, body limits, pagination limits, per-user/service rate limits, timeouts, bounded retries and circuit breakers. Retry only idempotent operations.
- Use additive versioned contract changes. Publish OpenAPI from this repository; generate/validate clients within their existing owning repositories rather than creating another repository. The fresh API container is published separately from the legacy API image as `ghcr.io/trendyzima/testagram-backend-fresh` using `Dockerfile.fresh`.

## Contract map

### Public user API (XClone -> Backend)

- `GET /v1/me`, `PUT /v1/me/profile`
- `GET /v1/feed/home`, `GET /v1/feed/following`
- `GET/POST/DELETE /v1/follows/{userID}` and `/v1/blocks/{userID}`
- `GET/POST/DELETE /v1/posts`, `/v1/posts/{postID}/reactions`, `/reposts`, `/bookmarks`, `/replies`
- `GET /v1/notifications`, `POST /v1/notifications/{id}/read`
- `POST /v1/media/uploads/reservations`, `POST /v1/media/uploads/{id}/complete`, `GET /v1/media/{id}/download`

Implemented in `internal/freshapi`: `/v1/me`, profile update, `/v1/feed/home`, and the fresh media upload/download routes. The home feed reads published Appwrite posts, applies follow/block/profile/post-visibility filters in Backend, and optionally sends only that bounded authorized candidate page to Algorithm. Follow mutations, blocks, post creation, reactions, reposts, bookmarks, replies, notifications and the dedicated following feed remain unimplemented. Keep those features fail-closed in XClone; do not fall back to Supabase.

### Private service API (Backend -> Algorithm)

- `POST /v1/feed/rank`; request includes only a bounded batch of eligible candidate IDs and ranking features. The Algorithm does not fetch content or user data.
- `GET /healthz`, `GET /readyz` for infrastructure probes only.

### Private service API (Backend -> Federated Content)

- Implemented in the Federation branch: `POST /internal/v1/activities` (private, idempotent, Backend-authorized activity enqueue). Backend now has a typed `internal/federationclient` publisher with HTTPS/private-host validation, a distinct service token, bounded payload/response handling and acknowledgement checks. **The Backend transactional-outbox dispatcher and producer wiring are still pending**, so this endpoint/client are not yet an end-to-end publish path. `GET /internal/v1/deliveries/{id}` and `POST /internal/v1/actors/resolve` remain planned.
- Federation must not create canonical users/posts or override Backend blocks, visibility, deletion or retention decisions.

### Private service API (Backend -> Data Centre)

- Use versioned authenticated infrastructure contracts for capacity reservation, media placement/replication, replica verification, restore, node enrollment/heartbeat and queue/rate-limit operations.
- Data Centre must not write canonical profiles/posts/social relationships. Storage metadata is authoritative only for physical placement; Backend/Appwrite remains authoritative for ownership and lifecycle state.

## Current integration status

- Implemented: Appwrite JWT verification, `/v1/me`, profile update, authenticated `/v1/feed/home`, fresh media upload/download API; Backend-to-Algorithm private rank client with bounded candidate validation and post-ranking authorization recheck (chronological fallback only when FEED_RANKING_REQUIRED=false); Algorithm private bearer-token rank endpoint; Data Centre node/infrastructure control plane.
- In progress: lifecycle workers/scanner and five-repository deployment wiring.
- Not implemented yet: the remaining social/feed mutation and notification APIs; Backend-to-Federation private API; full Backend-to-Data-Centre media placement/replica API; production deployment topology; cross-repository end-to-end tests with live Appwrite, Algorithm, Federation, Data Centre and object-provider credentials.
- Federation readiness intentionally returns unavailable until signature verification and durable inbox/outbox are safe.
- Supabase is not part of the fresh runtime path. Legacy Supabase projects/data are out of scope and must remain untouched. No fresh-mode fallback to Supabase is permitted.

## Release gates

1. Contract tests for each API and auth failure/expiry/revocation cases.
2. Cross-repository tests: XClone -> Backend -> Appwrite; Backend -> Algorithm; Backend -> Data Centre; Backend -> Federation (when enabled).
3. Request ID propagation, idempotency replay, timeout/retry, rate-limit and credential rotation tests.
4. Verify public frontend bundle and runtime network traces contain no Supabase API requests or credentials in fresh mode.
5. Verify no repository besides Backend writes canonical product records.
6. No production cutover, destructive retention or federation activation until the applicable gates pass.
