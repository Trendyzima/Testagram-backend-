# Testagram service ownership catalogue

This catalogue reflects the current six-runtime-repository architecture and the separate build-time API contract control plane. Testagram Backend is the sole authority for product identity, sessions, authorization and canonical product state.

## Repository ownership

| Repository | Owns | Explicit boundary |
|---|---|---|
| `testagram-flutter-client-` | Flutter UI, navigation, local presentation state and native Testagram session client | Calls the public Backend API only; no direct database, storage-provider or internal-service access |
| `Testagram-backend-` | Native authentication, canonical PostgreSQL product state, privacy/authorization, social API, media lifecycle, orchestration and outboxes | Sole user identity/session authority; does not delegate product authorization |
| `Testagram-data-centre-` | VPS/runtime deployment, private infrastructure API, storage nodes, video/notification services, backup and operations | Operates infrastructure; does not own product identity, user sessions or content authorization |
| `cdn-` | Media/HLS/DASH delivery, cache, origin/shield routing, range handling and playback-token validation | Delivery plane only; no product authorization or account identity |
| `federated-content` | ActivityPub actors, keys, inbox/outbox, signature verification, remote delivery and federation state | Cannot override local account/content authorization |
| `the-algorithm` | Stateless feed ordering over an already-authorized bounded candidate set | Cannot broaden candidate visibility or write canonical product data |
| `testagram-api` (control plane; not runtime) | OpenAPI, private contracts, event schemas, SDKs, source pins and cross-repository compatibility CI | Build-time contract authority only; no runtime requests or authorization decisions |

## Service contracts

| Capability | Owner | Persistence / API | Boundary |
|---|---|---|---|
| User authentication | Backend | `testagram_auth.identities`, `testagram_auth.sessions`, `/api/auth/*` | Backend issues and validates native access/refresh credentials; no external session fallback |
| Identity applications and review | Backend | `testagram_identity.*`, native reviewer UUIDs, dual-control approval and evidence lifecycle | Public intake remains fail-closed until staff WebAuthn readiness and evidence-service acceptance pass |
| Product data | Backend | PostgreSQL schemas under `testagram_local`, `testagram_auth`, `testagram_identity` | Backend migrations, least-privilege roles, backup and restore |
| Media metadata and authorization | Backend | `testagram_media.assets`, upload intents, replica/deletion ledgers | Backend owns owner, visibility, quotas, checksum and deletion state |
| Physical media storage | Data Centre + configured providers | Private S3-compatible object stores, R2/B2 replicas and replica evidence | Private by default; signed access and read-back checksum verification |
| Media delivery | CDN | Stable media API, HLS/DASH and origin routing | Backend-issued viewer/object authorization; no product authorization |
| Rate limiting | Backend | PostgreSQL counters | Fail closed for sensitive writes; bounded expiry cleanup |
| Durable asynchronous work | Owning service | PostgreSQL transactional outbox/lease tables | Idempotency keys, bounded retries, reconciliation and oldest-pending age |
| Feature configuration and platform events | Data Centre | Private platform API and PostgreSQL platform tables | Private service credentials; not a user-facing product API |
| Video processing | Data Centre | Video API, persistent media root and FFmpeg workers | Readiness, quota, HLS playback and worker recovery |
| Notifications | Data Centre | Device registration, preferences and delivery state | Backend-authorized events and scoped internal producer credentials |
| Feed ranking | The Algorithm | Private bounded ranking API | Backend supplies authorized candidates and rechecks visibility after ranking |
| Federation | Federated Content | Signed inbox/outbox and remote delivery | Backend authorizes local actors/content; signature, consent, replay and deduplication gates apply |
| Hosting control plane | Backend + Data Centre | Private deployment API, immutable image digests and node allowlists | Explicit operator authorization and auditable rollout |
| Monitoring | Backend + Data Centre | Prometheus-compatible metrics, private dashboards and alerts | Private observability endpoints; never expose secrets or private media URLs |

## Native authentication contract

- Flutter signs up, logs in, refreshes and logs out through Backend native auth routes.
- Backend owns password hashing, email verification, token issuance, refresh rotation, revocation, disabled/reset-required state and canonical Testagram UUIDs.
- Every protected request validates the native token and rechecks current PostgreSQL account/session state.
- Internal services use distinct, scoped service credentials. End-user access tokens are never reused as service credentials.
- Historical external identity identifiers are inert audit metadata. They cannot authenticate, authorize, or establish account ownership; account linking requires explicit verified reconciliation, never email equality alone.

## Health and API conventions

- Use each service's documented liveness and readiness endpoints; readiness must include required dependencies, not merely process liveness.
- Public endpoints use HTTPS. Private service traffic uses the private service network and bounded JSON contracts.
- Propagate request IDs across service hops. Never log passwords, JWTs, service tokens, signed URLs or raw authorization headers.
- Enforce strict schemas, body/batch limits, request deadlines, bounded concurrency and bounded retries.
- Retry only idempotent operations or operations protected by an idempotency key.
- Preserve privacy, blocks, moderation and deletion tombstones across every projection and delivery path.

## Production acceptance gates

Require all runtime repository CI workflows, native signup/email verification/login/refresh/logout tests, staff WebAuthn enrollment/assertion/recovery and dual-control tests, PostgreSQL privacy integration, media checksum/ownership/replication/deletion tests, ranking candidate-subset tests, federation signature/consent/deduplication tests, backup/restore drills and a real device-to-playback test. A route, schema or placeholder is not a completed feature.
