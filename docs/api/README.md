# Testagram API layer (canonical contract)

This repository owns the only client-facing product API. There are exactly five repositories; do not create a sixth API repository.

## Origins and trust boundaries
- Public API: `https://api.testagram.site/v1` (XClone -> Backend).
- Private APIs: `/internal/v1` or service-local `/v1` endpoints reachable only on the private service network.
- Appwrite Auth verifies human sessions. Backend derives caller ID from the verified JWT; client-supplied IDs are selectors, never authority.
- Backend uses separate service-scoped credentials for Algorithm, Federation and Data Centre. Never reuse a user JWT as a service token.
- Algorithm ranks bounded Backend-authorized candidates; Backend rechecks visibility and hydrates canonical records.
- Federation owns ActivityPub protocol state only. Backend owns local identity, authorization and post lifecycle.
- Data Centre owns infrastructure and physical storage operations only. Backend owns media ownership, visibility, quotas and lifecycle decisions.

## Wire protocol
- JSON UTF-8, UTC RFC3339 timestamps.
- Every Backend response includes `X-Testagram-API-Version: 1` and a safe `X-Request-ID`. The server replaces malformed request IDs.
- Mutations use `Idempotency-Key`; reuse with a different payload returns HTTP 409.
- Standard success envelope where applicable: `{ "ok": true, "data": ..., "error": null }`.
- Standard error shape: `{ "ok": false, "error": { "message": "..." } }`. HTTP status remains authoritative.
- Private responses are no-store. Never log credentials, signed URLs, private message bodies or sensitive payloads.
- Strict body limits, pagination bounds, schema validation, timeouts and fail-closed authorization are mandatory.

## Implemented public Backend routes
| Route | Purpose | Status |
|---|---|---|
| `GET /v1/me` | Current authenticated profile | Implemented |
| `PUT /v1/me/profile` | Update profile | Implemented |
| `GET/PUT /v1/me/legal` | Read/update legal consent | Implemented |
| `GET/PUT /v1/me/federation` | Read/update federation preference | Implemented |
| `GET /v1/feed/home` | Authorized home feed with optional private ranking | Implemented |
| `GET /v1/feed/public` | Public feed | Implemented |
| `POST /v1/posts` | Idempotent text post creation with moderation | Implemented |
| `POST /v1/moderation/check` | Content moderation preflight | Implemented |
| `POST /v1/media/uploads/reservations` | Reserve upload and quota | Implemented when media is enabled |
| `POST /v1/media/uploads/{reservationID}/complete` | Verify/finalize upload | Implemented when media is enabled |
| `GET /v1/media/{objectID}/download` | Authorized signed download | Implemented when media is enabled |
| `POST /v1/media/{objectID}/restore` | Owner-authorized restore | Implemented when media is enabled |

Follow, block, reactions, reposts, bookmarks, replies, messages, calls, audio spaces, wallet APIs and notifications must not be represented as live until Backend handlers, persistence, authorization and contract tests exist. XClone must fail closed rather than call a legacy database, Edge Function or private service directly.

## Private service contracts
| Caller -> owner | Contract | Boundary |
|---|---|---|
| Backend -> Algorithm | `POST /v1/feed/rank` | Up to 200 authorized candidate IDs; ranker returns ordering/scores only |
| Backend -> Federation | `POST /internal/v1/activities`; Backend exposes private actor and durable outbox reads | Federation cannot create or authorize local product records |
| Backend -> Data Centre | Existing private platform API for cache/rate limits/queues; media placement/replica API remains a separate implementation gate | Infrastructure only; no canonical social data writes |

## Source of truth
Canonical OpenAPI: [fresh-platform-api.openapi.yaml](../fresh-platform-api.openapi.yaml). Update it with every public route. Private service contracts remain in their owning repositories. No API client, service credential or database secret belongs in XClone.

## Release gates
1. OpenAPI validates and every documented live route has a handler and auth test.
2. Request IDs, version headers, idempotency replay and malformed-input tests pass.
3. Backend -> Algorithm, Federation and Data Centre calls use independent credentials, bounded timeouts and response validation.
4. XClone bundle has no service tokens, direct database access or private-service URLs.
5. Cross-repository staging E2E verifies login, feed, post, media upload/download, and only then optional federation.
