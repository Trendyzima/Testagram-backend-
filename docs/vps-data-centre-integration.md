# VPS Data Centre integration contract

The [Testagram Data Centre](https://github.com/Trendyzima/Testagram-data-centre-)
owns VPS assembly, storage/node operations, video processing, notifications
and platform infrastructure. This repository owns the canonical product API,
authorization, PostgreSQL data model, media metadata and service contracts.

## Cross-repository release gates

- Backend, Flutter and Data Centre services use canonical Testagram UUIDs for product identity.
- Flutter creates and authenticates accounts through the Testagram Backend native auth API. Backend-issued access and refresh credentials are validated against Testagram PostgreSQL account/session state.
- Video and notification services receive only the scoped, signed service contracts they require; they do not validate user bearer credentials or become independent identity authorities.
- Storage services use signed, bounded upload/download contracts and durable PostgreSQL deletion jobs.
- The Data Centre platform API is PostgreSQL-backed configuration, telemetry and audit only.
- Node workloads require immutable image digests, an exact allowlist and explicit workload-execution capability from the node agent.
- Federation remains unavailable for production until signature verification, consent, durable inbox/outbox, deduplication and actor resolution pass.

## Deployment contract

Configure the canonical Testagram PostgreSQL connection and the required internal service credentials for Backend and Data Centre workloads. User authentication remains owned by the Backend native auth service; no external identity endpoint, project identifier or provider API key is required.

The Data Centre Compose stack must pass PostgreSQL readiness before starting identity-dependent services. A local PostgreSQL schema migration is required before deploying application code that depends on new columns or tables.

## Recovery and verification

Before production enablement:

- Run Go tests, race detector, vet/build, Compose validation and all required repository CI workflows on the exact release commits.
- Verify native signup, email verification, password login, refresh rotation, logout, disabled-account rejection and unknown Testagram identity rejection.
- Verify staff authorization with native staff principals, role grants and backend-verified WebAuthn/MFA elevation.
- Test cross-account media ownership, private media access, quota reservation, checksum mismatch, concurrent upload completion and idempotent deletion.
- Back up PostgreSQL and object storage off-host; restore into an isolated environment and verify login, upload, private-media retrieval and playback.
- Test node-agent path confinement, immutable workload allowlists, timeout and cancellation cleanup against the supported container runtime.
- Confirm database saturation, outbox age, media deletion backlog and storage capacity alerts before enabling workloads.

## Ownership

| Area | Source of truth |
|---|---|
| Product API, native authentication, social authorization, PostgreSQL schema and media metadata | Testagram Backend |
| VPS assembly, storage nodes, video/notification services and operations | Testagram Data Centre |
| Native registration, sign-in, refresh and session lifecycle UI | Flutter client |
| Feed ranking | The Algorithm |
| Signed federation inbox/outbox and remote delivery | Federated Content |

## Infrastructure target

See [Instabox VPS + Cloudflare R2 + Backblaze integration](instabox-cloudflare-b2-integration.md) for the canonical VPS PostgreSQL placement, three-copy media durability target, custom CDN origin policy and analytics ownership.
