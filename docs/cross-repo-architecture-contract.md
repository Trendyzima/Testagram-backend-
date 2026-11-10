# Testagram five-repository integration contract

Status: target architecture and staging contract; not a production cutover authorization.

This document defines the runtime ownership and API boundaries across the five retained repositories: [Flutter client](https://github.com/Trendyzima/testagram-flutter-client-), [Testagram Backend](https://github.com/Trendyzima/Testagram-backend-), [Data Centre](https://github.com/Trendyzima/Testagram-data-centre-), [The Algorithm](https://github.com/Trendyzima/the-algorithm), and [Federated Content](https://github.com/Trendyzima/federated-content). Legacy XClone is a migration source, not a runtime service. This contract supplements the [replication transport contract](dual-backend-replication-transport.md) and [virtual storage contract](platform/virtual-storage.md).

## Ownership boundaries

- **XClone** owns the product UI, browser/mobile session acquisition, client-side API adapters, and compatibility with existing product features.
- **Hybrid migration target:** Appwrite Auth owns credentials/sessions after staged linking; independent PostgreSQL is the canonical Testagram product and federation data plane. Supabase remains a recoverable legacy source for each domain until that domain passes its migration gate. Never infer cutover from a mirrored record or successful health probe.
- **Testagram Go API** owns its documented gateway behavior, bounded request handling, operational endpoints, and explicitly migrated backend capabilities. A gateway response is not proof that every product domain has migrated.
- **Replication transport** provides authenticated, bounded, at-least-once event delivery with idempotent intake. A transport acknowledgement means durable peer acceptance, not successful product projection.
- **Projection tables** are read models only. They must not silently replace the source tables or grant write authority.
- **Object storage/CDN** owns binary transfer and delivery. The application API authorizes lifecycle transitions; private objects must never be exposed via public CDN paths.
- **Supabase is migration-source-only**: no Supabase authentication, database, storage, realtime, SDK, credentials, URLs, or runtime fallback may remain in the five retained repositories or their deployment configuration.
- **Upstash and Redis/Valkey are excluded from the target architecture**: do not add clients, credentials, URLs, cache wrappers, or service dependencies. Use native PostgreSQL and the existing bounded service contracts where persistence or coordination is required.

## Replication v1 wire contract

The event envelope is defined by XClone's `contracts/replication-event-v1.schema.json` and the matching Go validator. Treat v1 as closed: reject unknown top-level fields, unsupported schema versions, invalid identifiers/timestamps, mismatched entity/event types, and envelopes larger than 1 MiB. Any future schema change requires a separately versioned contract and coordinated compatibility tests before producers emit it.

Required fields: `id`, `origin`, `entity_type`, `entity_id`, `entity_version`, `schema_version`, `event_type`, `occurred_at`, and `payload`.

- `schema_version` is exactly `1` for v1.
- `entity_version` is a positive, monotonically increasing version for one entity.
- `event_type` must be prefixed by `entity_type + "."`.
- The signed bytes must be the exact transmitted body; authenticate origin and timestamp, reject stale timestamps, and compare duplicate event IDs against the original envelope.
- Payloads must be schema-specific allowlists. Generic sensitive-key rejection is defense in depth, not a substitute for projection allowlists.
- Never export auth/session credentials, personal contact details, private content, signed/upload URLs, or provider secrets.
- Deletes and privacy changes require tombstones or equivalent terminal state so stale events cannot resurrect hidden/deleted records.

## Delivery and consistency semantics

1. Source mutation and its outbox event must commit in the same database transaction.
2. Relays use bounded leases, bounded batches, request deadlines, retry/backoff, and idempotency.
3. A peer acknowledgement is issued only after the event is durably accepted.
4. Inbox projection is independently idempotent and applies only if the incoming version is newer and the entity remains eligible for the target projection.
5. Terminal validation/privacy failures are dead-lettered and observable; transient failures are retried. Never retry forever without a cap or hide permanent failures.
6. Snapshot seeding must be bounded and safe to rerun. It must not race entity version allocation or overwrite a newer event.
7. Reconciliation compares source and projection state and reports differences before any repair. Repairs must preserve tombstones and privacy restrictions.

## API and media compatibility

- Validate Appwrite JWTs server-side and resolve them through the explicit identity bridge. Never accept a client-supplied account ID as authenticated identity, use a service-role credential to impersonate a user, or bypass authorization. Supabase must not be used as a runtime fallback for any retained capability.
- Browser configuration may contain only publishable values. Backend origins, signing keys, database credentials, and internal platform tokens remain server-side.
- Preserve existing client response/error shapes or add an explicit versioned adapter. Unknown endpoints fail clearly; do not silently switch data stores.
- Upload lifecycle must be tested end-to-end: authenticated intent → signed upload → provider-observed verification → completion → authorized attachment → public CDN rendering or private retrieval → deletion and cleanup.
- Tenant identity comes from verified server authentication, not a caller-supplied owner ID. Public/private buckets and cache policies must be separated.
- Keep virtual storage and replication workers disabled by default until their own release gates pass.

## Required release evidence

- [ ] Contract tests run against the actual Go validator, TypeScript/Edge validator, and PostgreSQL functions/migrations.
- [ ] Unsupported schema, mismatched event type, oversize envelope, malformed timestamp, replay, changed duplicate ID, and sensitive nested keys are rejected consistently.
- [ ] Public eligible records project; private/community records do not.
- [ ] Privacy transition and delete tombstones prevent stale-event resurrection.
- [ ] Peer outage, retry, lease expiry, duplicate delivery, dead-letter inspection, and reconciliation are exercised using disposable databases.
- [ ] Media lifecycle, cross-tenant denial, expired URLs, checksum/type verification, cleanup retries, and CDN cache behavior pass against a real staging object provider.
- [ ] Flutter client, Backend, Data Centre, Algorithm, and Federated Content CI pass on the exact coordinated commits proposed for rollout.
- [ ] Dependency-boundary scans confirm no Supabase, Upstash, or Redis runtime references, credentials, or service configuration in the retained five-repository architecture.
- [ ] Staging verifies Auth/RLS, rollback to the old route, dashboards, and alert delivery.
- [ ] Production migrations are backed up, reviewed, and applied separately; CI integration does not prove production migration success.

## Rollout rule

Migrate one capability family at a time. Record its source of truth, contract, authorization rule, metrics, regression tests, staging evidence, feature flag, and rollback. Do not enable global dual-write, replication workers, virtual storage, or DNS changes as a side effect of merging documentation or passing builds.
