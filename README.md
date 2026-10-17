# Testagram Backend

This repository is the application-service foundation for Testagram. It is separate from:

- supabase-for-testagram: Supabase platform and database/auth/storage services.
- cdn-: Go media-delivery/cache data plane.

It must not duplicate Supabase's database, authentication, or binary media responsibilities. It provides a separately deployable API/worker layer that can scale horizontally as demand grows.

## Current service

The initial Go service provides:

- GET /healthz: process liveness only; does not depend on the database.
- GET /readyz: readiness check with a bounded PostgreSQL ping; returns 503 if the database is not ready.
- GET /metrics: minimal Prometheus text-format request counter.
- POST /api/capability: Testagram capability gateway compatible with the current web client contract.
- Bounded PostgreSQL connection pool (maximum 20 per instance by default).
- Request timeouts, header limit, and graceful shutdown.
- Non-root, multi-stage container build.
- Isolated local Docker Compose stack and CI checks.

This is a foundation, not a claim of production readiness or a 5-million-concurrent-user capacity result. Product endpoints should be added only after the existing Testagram contracts and database schema have been audited.

## Testagram compatibility gateway

`POST /api/capability` implements the same capability-dispatch envelope used by the existing Testagram web client. It forwards the user's Bearer JWT and the publishable API key to the canonical Supabase `capability_dispatch_v2` RPC; it does not use a service-role key to bypass row-level security. It also preserves the bookmark RPC compatibility paths, applies strict origin allow-listing, caps request/response bodies, applies an 8-second upstream deadline, and returns request IDs on gateway errors. `GET /healthz`, `GET /readyz`, and `GET /metrics` remain available.

To opt the web app into this gateway only after deployment, set `VITE_TESTAGRAM_API_URL=https://api.testagram.site` in the frontend build environment. Leave it blank to keep the current same-origin `/api/capability` Vercel route. Configure matching TLS/DNS and `ALLOWED_ORIGINS` before switching production traffic. The gateway requires `SUPABASE_URL` and `SUPABASE_PUBLISHABLE_KEY`; never set the Supabase service-role key in browser-visible configuration.

## Local development

Requirements: Go 1.23+, Docker Compose.

1. Copy .env.example to .env.
2. Set POSTGRES_PASSWORD to a strong local-only password.
3. Set DATABASE_URL to use the same password, e.g. postgres://testagram:YOUR_PASSWORD@db:5432/testagram?sslmode=disable
4. Start the stack with docker compose up --build.
5. Verify GET http://127.0.0.1:8080/healthz, /readyz and /metrics.

The local database is for development only and is not automatically wired to the live Supabase project. Do not put production credentials in this repository or in .env.example.

## Production integration rules

- Inject secrets through deployment secrets; never bake them into images.
- Keep PostgreSQL private. Use the supported Supabase connection/pooler endpoint where appropriate.
- Use a least-privilege database role for runtime queries; never use the Supabase service-role key in browser code.
- Put public media delivery through the Testagram CDN and object origin; do not stream large media through this API.
- Keep long-running federation and notification tasks in bounded, independently scalable workers with idempotent jobs and retry limits.
- Add request-level authentication/authorization and rate limits before exposing business endpoints.
- Add dashboards and alerts for p95/p99 latency, 5xx rates, database pool saturation, queue age, CPU, memory and network saturation.
- Keep production data and local test data separate. Test migrations against staging before applying them to production.

## Scale strategy

Read docs/architecture.md and docs/operations.md. Scale by measured bottleneck: CDN/cache for media, stateless API replicas for request throughput, bounded workers for asynchronous work, and database tuning/replicas when query metrics justify them. Registered account count alone is not a capacity metric.
