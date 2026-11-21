# Testagram Backend API contract

Testagram Backend owns the public product API, canonical user identity, sessions, product authorization and PostgreSQL product state. Flutter uses this API for all user-authenticated product operations. Internal services receive scoped service credentials and cannot become alternate user-identity authorities.

## Origins and trust boundaries

- Public API origin: `https://api.testagram.site`.
- Production API runtime: `cmd/api`.
- Private service APIs are reachable only over the approved private service network.
- Backend issues native access and refresh credentials. Protected requests validate the access token and recheck current PostgreSQL account/session state.
- Client-supplied user IDs are selectors only; Backend derives the authenticated principal from the validated native session.
- Backend uses separate service-scoped credentials for Algorithm, Federation, Data Centre and CDN-origin authorization. Never reuse an end-user JWT as a service credential.
- Algorithm ranks only bounded Backend-authorized candidates; Backend rechecks visibility and hydrates canonical records.
- Federation owns ActivityPub protocol state only. Backend owns local identity, authorization and post lifecycle.
- Data Centre owns infrastructure and physical storage operations only. Backend owns media ownership, visibility, quotas and lifecycle decisions.
- CDN is a delivery plane and cannot grant product access.

## Native authentication routes

The production router registers these Testagram-native account/session operations:

| Route | Purpose |
|---|---|
| `POST /api/auth/signup` | Create a native registration/account request under the configured registration gate |
| `POST /api/auth/resend-verification` | Resend native email verification |
| `POST /api/auth/verify-email` | Verify the email challenge |
| `POST /api/auth/login` | Verify password and issue native session credentials |
| `POST /api/auth/refresh` | Rotate the opaque refresh credential |
| `POST /api/auth/logout` | Revoke the current session |
| `POST /api/auth/logout-all` | Revoke all sessions for the account |
| `POST /api/auth/forgot-password` | Start anti-enumerating password recovery |
| `POST /api/auth/reset-password` | Consume a single-use reset challenge and revoke prior sessions |

Native identity application, private evidence, review, approval and provisioning handlers live under `/api/identity/*` and `/api/admin/identity/*`. Public identity intake remains fail-closed while `identityReviewWebAuthnReady` is false; a route existing in source is not proof that production registration is enabled.

## Product and media routes

- Current profile operations use `/api/me/profile`.
- Home and public feed routes use `/api/feed/home` and `/api/feed/public`; the production router also retains `/v1/feed/home` and `/v1/feed/public` compatibility paths.
- Social post creation uses the native social API under `/api/social/posts`; moderation is enforced within native post creation. There is no standalone legacy moderation preflight route.
- Native media upload, completion, private delivery, restore, replication and lifecycle handlers are implemented in `cmd/api/media.go` and `cmd/api/fresh_media_bridge.go`. Exact route contracts must be taken from the current router and the shared API contract control plane, not from superseded route lists.

Do not restore removed legacy `/v1/me`, `/v1/me/legal`, `/v1/moderation/check`, `/v1/posts` or old media paths as aliases without a reviewed client migration and contract tests.

## Private service contracts

| Caller -> owner | Contract | Boundary |
|---|---|---|
| Backend -> Algorithm | Private bounded feed-ranking API | Ranker returns only ordering/scores for submitted candidates |
| Backend <-> Federated Content | Signed actor, activity, inbox/outbox and delivery contracts | Federation cannot create or authorize local product records |
| Backend -> Data Centre | Private platform and media lifecycle APIs | Infrastructure and physical media operations only |
| Backend -> CDN | Viewer/object-bound media assertions and origin authentication | CDN validates delivery capability; Backend owns product visibility |

## Wire and security requirements

- JSON over HTTPS for public APIs; private service traffic uses the approved private network and bounded JSON contracts.
- Use UTC RFC3339 timestamps and propagate a safe request ID across service hops.
- Mutations use idempotency keys where the operation contract supports replay protection.
- HTTP status remains authoritative; responses must not disclose secrets or private data.
- Enforce strict body limits, pagination bounds, schema validation, timeouts, concurrency limits and fail-closed authorization.
- Private responses are no-store. Never log passwords, JWTs, service tokens, signed URLs, private message bodies or raw authorization headers.
- Mobile/web clients never receive service credentials or database URLs.

## Source of truth and release gates

The shared `testagram-api` control plane owns OpenAPI, private schemas, event contracts, SDKs, source pins and cross-repository compatibility CI. Backend handlers remain the implementation source for actual route behavior. Update the shared contract with every accepted route change.

Before a route is described as live, require a production handler, authorization tests, persistence tests, error/timeout behavior and cross-repository contract coverage. Production acceptance additionally requires native signup/email verification/login/refresh/logout tests, staff WebAuthn and dual-control tests, media ownership/checksum/deletion tests, federation signature/consent/deduplication tests, backup/restore drills and deployed staging verification.
