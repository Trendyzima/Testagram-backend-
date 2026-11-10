# Testagram Service Ownership Catalogue

This catalogue describes the retained five-repository architecture. The
canonical product database is PostgreSQL. Appwrite issues identity sessions;
the Backend validates Appwrite JWTs and owns product authorization.

## Five-repository ownership

| Repository | Owns | Explicit boundary |
|---|---|---|
| Flutter client | Native screens, navigation, Appwrite session acquisition | No direct database, object-store, ranker or federation access |
| Testagram Backend | Public API, privacy/authorization, canonical PostgreSQL, media lifecycle, rate limits and hosting control plane | No Supabase/PostgREST runtime, no Redis/Upstash |
| Data Centre | Internal platform API, video/notification services, storage nodes, workers and VPS operations | Uses shared PostgreSQL identity bridge; does not own product authorization |
| The Algorithm | Feed ranking over Backend-authorized candidates | Cannot broaden the authorized candidate set or write product data |
| Federated Content | Signed inbox/outbox, remote policy and delivery | Cannot override local account/content authorization |

## Service contracts

| Capability | Owner | API / persistence | Health and recovery |
|---|---|---|---|
| Authentication | Appwrite | Appwrite sessions and JWTs | Backend validates Account endpoint; unknown/disabled identities fail closed |
| Product data | Backend | PostgreSQL schemas under `testagram_local`, `testagram_auth`, `testagram_identity` | PostgreSQL readiness, migrations, backups and restore drills |
| Media metadata | Backend | `testagram_media.assets`; `posts.media_ids` stores native object keys | Signed upload/download, checksum, quotas and durable deletion jobs |
| Rate limiting | Backend | PostgreSQL fixed-window counters | Fail closed for sensitive writes; bounded expiry cleanup |
| Durable asynchronous work | Owning service | PostgreSQL transactional outbox/lease tables | Idempotency keys, bounded retries, reconciliation and oldest-pending age |
| Feature configuration | Data Centre | Internal platform API and PostgreSQL `testagram_platform.feature_flags` | Private bearer token, /readyz and DB readiness |
| Events and audit | Data Centre | Internal platform API and PostgreSQL events/audit tables | Private bearer token, retention and metrics |
| Video processing | Data Centre | Video API, persistent media root and FFmpeg worker | /healthz, /readyz, quota, HLS playback and worker recovery |
| Notifications | Data Centre | Device registration, preferences and delivery state | Appwrite JWT validation, internal producer token and idempotent delivery |
| Feed ranking | The Algorithm | Private ranker contract called by Backend | Bounded requests, candidate-subset validation and pagination tests |
| Federation | Federated Content | Signed inbox/outbox and remote delivery | Signature verification, consent rechecks, deduplication and durable retry |
| Hosting control plane | Backend | `openapi/hosting-control-plane.yaml`, PostgreSQL hosting schema | Private operator API, active-domain verification and restore tests |
| Object storage | Backend + Data Centre | S3-compatible boundary and node inventory | Private by default, signed access, checksums, capacity and restore tests |
| Monitoring | Backend + Data Centre | Prometheus-compatible metrics, Caddy, Grafana | Private observability ports and explicit alerting |

## Dependency policy

- No Supabase Auth, PostgREST, Storage, Realtime or Edge Function runtime.
- No Redis/Valkey/Upstash clients, URLs, credentials, cache wrappers or generic
  Redis queue APIs.
- PostgreSQL owns rate-limit counters, product data and durable outbox state.
- Public immutable media may use HTTP/CDN caching; cache is never authoritative.
- External payment, email and federation peers are explicit adapters with
  durable local ledger/retry state.

## Health and API conventions

- `/healthz`: process liveness.
- `/readyz`: required dependencies ready.
- `/metrics`: private-network-only operational metrics.
- Internal service APIs use private bearer credentials and bounded JSON bodies.
- Mobile/web clients never receive service credentials or direct database URLs.

## Production cutover gates

Require all five repository CI workflows, Appwrite session/identity tests,
PostgreSQL privacy integration tests, media checksum/ownership tests, ranking
pagination tests, federation signature/deduplication tests, backup/restore
drills and a real device-to-playback video test. A route or placeholder is not
a completed feature.
