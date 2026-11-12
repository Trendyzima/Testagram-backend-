# Testagram Backend

The native Go application API and PostgreSQL data plane for Testagram.

## Six-runtime-repository ownership + API contract control plane

- [Flutter client](https://github.com/Trendyzima/testagram-flutter-client-): native Android/web UI; Appwrite session acquisition; all product data through this API.
- **Testagram Backend (this repository):** Appwrite JWT verification, authorization, canonical PostgreSQL data, social APIs, media lifecycle and service orchestration.
- [Data Centre](https://github.com/Trendyzima/Testagram-data-centre-): storage nodes, video processing, notifications, platform infrastructure and operations.
- [The Algorithm](https://github.com/Trendyzima/the-algorithm): feed ranking over Backend-authorized candidate sets.
- [Federated Content](https://github.com/Trendyzima/federated-content): signed inbox/outbox, federation policy and remote delivery.
- **Testagram API (`testagram-api`):** OpenAPI contracts, private service schemas, event definitions, SDKs, immutable source pins and cross-repository compatibility CI. This is a contract control plane, not a runtime service or product-data authority.

## Runtime boundaries

- Appwrite is the only client identity provider. The Backend validates Appwrite session JWTs against the Appwrite Account API and resolves the Appwrite ID through the PostgreSQL identity bridge.
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

Requirements: Go version declared by `go.mod`, PostgreSQL 17+, and Appwrite project credentials.

1. Copy `.env.example` to `.env` and configure `DATABASE_URL`, `APPWRITE_ENDPOINT` and `APPWRITE_PROJECT_ID`.
2. Apply migrations in order. The self-hosted Compose stack mounts the complete ordered migration set.
3. Run `go test ./...` from the repository root and `go test ./...` from `services/platform`.
4. Start the API with `go run ./cmd/api`.

## Self-hosted deployment

See [deploy/self-hosted](deploy/self-hosted/README.md). The stack uses PostgreSQL,
S3-compatible object storage, the Go API, the PostgreSQL-backed platform API,
control-plane services, the Appwrite-authenticated staff console, and monitoring.
It does not deploy Supabase, Redis/Valkey or Upstash.

## Migration status

The migration ledger is the source of truth for completed native Flutter slices.
A route is not complete merely because it navigates or renders a placeholder.
Cross-repository CI, PostgreSQL integration tests, Appwrite authentication tests
and end-to-end API contracts must pass before a feature is marked complete.
