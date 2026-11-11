# VPS Data Centre integration contract

The [Testagram Data Centre](https://github.com/Trendyzima/Testagram-data-centre-)
owns VPS assembly, storage/node operations, video processing, notifications
and platform infrastructure. This repository owns the canonical product API,
authorization, PostgreSQL data model, media metadata and service contracts.

## Cross-repository release gates

- Backend and Data Centre use the same canonical PostgreSQL identity bridge.
- Flutter obtains Appwrite sessions; Backend, video and notification services
  validate Appwrite JWTs and resolve canonical Testagram UUIDs.
- Storage services use signed, bounded upload/download contracts and durable
  PostgreSQL deletion jobs.
- The Data Centre platform API is PostgreSQL-backed configuration, telemetry
  and audit only. It is not a Redis/Valkey/Upstash cache or generic queue.
- Node workloads require immutable image digests, an exact allowlist and
  explicit workload-execution capability from the node agent.
- Federation remains unavailable for production until signature verification,
  consent, durable inbox/outbox, deduplication and actor resolution pass.

## Deployment contract

Configure the same Appwrite endpoint/project ID and canonical PostgreSQL
connection for Backend and Data Centre services that need identity resolution.
Keep server-only Appwrite API keys, storage credentials and internal service
tokens in the protected host environment.

The Data Centre Compose stack must pass PostgreSQL readiness before starting
identity-dependent services. A local PostgreSQL schema migration is required
before deploying application code that depends on new columns or tables.

## Recovery and verification

Before production enablement:

- Run Go tests, race detector, vet/build, Compose validation and all five
  repository CI workflows on the exact release commits.
- Verify Appwrite session rejection, disabled-account rejection and unknown
  identity mapping behavior.
- Test cross-account media ownership, private media access, quota reservation,
  checksum mismatch, concurrent upload completion and idempotent deletion.
- Back up PostgreSQL and object storage off-host; restore into an isolated
  environment and verify login, upload, private-media retrieval and playback.
- Test node-agent path confinement, immutable workload allowlists, timeout and
  cancellation cleanup against the supported container runtime.
- Confirm database saturation, outbox age, media deletion backlog and storage
  capacity alerts before enabling workloads.

## Ownership

| Area | Source of truth |
|---|---|
| Product API, social authorization, PostgreSQL schema and media metadata | Testagram Backend |
| VPS assembly, storage nodes, video/notification services and operations | Testagram Data Centre |
| Native product UI and Appwrite session acquisition | Flutter client |
| Feed ranking | The Algorithm |
| Signed federation inbox/outbox and remote delivery | Federated Content |

Supabase, PostgREST, Redis/Valkey and Upstash are not part of the retained
runtime. Do not restore those dependencies as shortcuts for cross-repository
integration.
\n\n## Infrastructure target\n\nSee [Instabox VPS + Cloudflare R2 + Backblaze integration](instabox-cloudflare-b2-integration.md) for the canonical VPS PostgreSQL placement, three-copy media durability target, custom CDN origin policy and analytics ownership.\n