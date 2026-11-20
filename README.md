# Testagram Backend

The native Go application API and PostgreSQL data plane for Testagram.

For the retired Appwrite runtime files and their Testagram-native replacement owners, see [`docs/architecture/native-replacement-ownership.md`](docs/architecture/appwrite-retirement-native-replacements.md).

## Six-runtime-repository ownership + API contract control plane

- [Flutter client](https://github.com/Trendyzima/testagram-flutter-client-): native Android/web UI; Testagram-native login/session acquisition; all product data through this API.
- **Testagram Backend (this repository):** Testagram-native access-token issuance and live session/account validation, authorization, canonical PostgreSQL data, social APIs, media lifecycle and service orchestration.
- [Data Centre](https://github.com/Trendyzima/Testagram-data-centre-): storage nodes, video processing, notifications, platform infrastructure and operations.
- [Testagram CDN](https://github.com/Trendyzima/cdn-): media delivery, edge caching and range/HLS transport; it does not authorize media access.
- [The Algorithm](https://github.com/Trendyzima/the-algorithm): feed ranking over Backend-authorized candidate sets.
- [Federated Content](https://github.com/Trendyzima/federated-content): signed inbox/outbox, federation policy and remote delivery.
- **Testagram API (`testagram-api`):** OpenAPI contracts, private service schemas, event definitions, SDKs, immutable source pins and cross-repository compatibility CI. This is a contract control plane, not a runtime service or product-data authority.

## Runtime boundaries

- Testagram-native authentication is the sole identity and session authority. Protected requests validate short-lived Testagram access tokens against live PostgreSQL session/account state; rotating opaque refresh tokens are accepted only by the refresh route. No external identity-provider JWT, session, SDK or API is part of the runtime. Historical identity reconciliation is an offline migration concern only.
- PostgreSQL is the canonical product database. The API does not proxy product reads/writes to Supabase or PostgREST.
- Media metadata and post references are native PostgreSQL records. `posts.media_ids` stores ordered `testagram_media.assets.object_key` values; no external media UUID bridge is used.
- Object bytes are stored through the virtual-storage/S3-compatible boundary. The Backend owns authorization, quotas, checksums, signed upload/download and durable deletion jobs.
- Capability rate limiting uses PostgreSQL fixed-window counters.
- Durable asynchronous work uses domain-owned PostgreSQL transactional outboxes and idempotent workers.
- Supabase, Redis/Valkey and Upstash are not runtime dependencies. Do not add their SDKs, credentials, URLs, cache wrappers or generic queue clients.
- No copied Supabase Edge Functions or browser PostgREST clients are part of the supported runtime.

## Important API surfaces

- `/api/social/*`: posts, profiles, followers/following, replies, reactions, reposts, bookmarks, polls and interaction lists.
- `/api/media/*`: upload intents, completion, attachment to native posts, private downloads and durable deletion.
- `/api/search`: privacy-filtered product search.
- `/api/capability`: bounded, allow-listed capability dispatch. Unimplemented capabilities fail closed; there is no Supabase RPC fallback.
- `/healthz`, `/readyz`, `/metrics`: health and operational metrics.

## Local development

Requirements: Go version declared by `go.mod` and PostgreSQL 17+. Use the variable names and safe defaults in `deploy/self-hosted/.env.example`; configure only Testagram-native authentication, PostgreSQL and approved service credentials.

1. Copy `deploy/self-hosted/.env.example` to the deployment environment and configure the native identity, database and service settings required by the selected local/staging profile.
2. Apply migrations in order. The self-hosted Compose stack mounts the complete ordered migration set.
3. Run `go test ./...` from the repository root and `go test ./...` from `services/platform`.
4. Start the API with `go run ./cmd/api`.

## Deployment authority

**Instabox VPS is the only supported deployment target. Render is retired and prohibited.** CI enforces this with `scripts/guard-no-render.py`; do not add retired-host manifests, workflows, URLs, credentials or fallback hosting.

## Self-hosted deployment

See [deploy/self-hosted](deploy/self-hosted/README.md). The stack uses PostgreSQL,
S3-compatible object storage, the Go API, the PostgreSQL-backed platform API,
control-plane services, the Testagram-native authenticated staff console, and monitoring.
It does not deploy Supabase, Redis/Valkey or Upstash.

## Migration status

The migration ledger is the source of truth for completed native Flutter slices.
A route is not complete merely because it navigates or renders a placeholder.
Cross-repository CI, PostgreSQL integration tests, native authentication tests
and end-to-end API contracts must pass before a feature is marked complete.


## Seven-repository Testagram integration

This repository is one component of a single Testagram product. The shared ownership map, runtime request flow, security boundaries, hostname ownership and release gates are maintained in the [Testagram API integration control plane](https://github.com/Trendyzima/testagram-api). That repository is build-time contract governance only; it is not a runtime gateway.

| Repository | Responsibility |
|---|---|
| [Flutter Client](https://github.com/Trendyzima/testagram-flutter-client-) | Native Android/web UI and public Backend transport |
| [Backend](https://github.com/Trendyzima/Testagram-backend-) | Canonical identity, sessions, authorization, PostgreSQL and public APIs |
| [Data Centre](https://github.com/Trendyzima/Testagram-data-centre-) | Private infrastructure, storage, processing, backups and restore |
| [CDN](https://github.com/Trendyzima/cdn-) | Authorized media byte delivery, edge cache and streaming transport |
| [Federated Content](https://github.com/Trendyzima/federated-content) | ActivityPub/WebFinger protocol state and remote delivery |
| [The Algorithm](https://github.com/Trendyzima/the-algorithm) | Private ranking of Backend-authorized candidates |
| [Testagram API](https://github.com/Trendyzima/testagram-api) | OpenAPI, private contracts, SDKs, immutable source pins and compatibility CI |

**This repository's boundary:** Canonical identity, sessions, authorization, PostgreSQL product records and public API orchestration.

**Contract entry points:** [Public API contract](https://github.com/Trendyzima/testagram-api/blob/main/contracts/openapi/testagram-v1.yaml) · [Private service contracts](https://github.com/Trendyzima/testagram-api/tree/main/contracts/internal/openapi)

**Shared integration references:** [Seven-repository ownership and request flow](https://github.com/Trendyzima/testagram-api/blob/main/docs/architecture/service-ownership.md) · [Locked end-to-end acceptance gates](https://github.com/Trendyzima/testagram-api/blob/main/docs/integration/locked-seven-repository-acceptance.md) · [Machine-readable service registry](https://github.com/Trendyzima/testagram-api/blob/main/contracts/registry/services.json)

Keep the ownership boundaries intact: Flutter calls only the public Backend API and approved media URLs; Backend remains the authority for user identity and product authorization; Data Centre owns storage mechanics; CDN transports bytes but does not authorize access; Federation owns protocol state but not local accounts or canonical posts; Algorithm ranks but does not authorize or hydrate product records. Never place private service credentials, database credentials or storage-provider keys in client code.
