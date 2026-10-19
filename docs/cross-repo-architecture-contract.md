# XClone ↔ Testagram cross-repository architecture contract

Status: implementation and staging contract; not a production cutover authorization.

This document defines the integration boundary between [XClone](https://github.com/Trendyzima/XClone-9b4dz1-9b4dz1-8936-20260809tr) and this Go application backend. It supplements, rather than replaces, the [hybrid integration contract](server-e-hybrid-integration.md), [replication transport contract](dual-backend-replication-transport.md), and [virtual storage contract](platform/virtual-storage.md).

## Ownership boundaries

- **XClone** owns the product UI, browser/mobile session acquisition, client-side API adapters, and compatibility with existing product features.
- **Supabase Auth/Postgres/Edge Functions** remain authoritative for the existing product operations until a domain is explicitly migrated and tested. Never assume a mirrored record is authoritative.
- **Testagram Go API** owns its documented gateway behavior, bounded request handling, operational endpoints, and explicitly migrated backend capabilities. A gateway response is not proof that every product domain has migrated.
- **Replication transport** provides authenticated, bounded, at-least-once event delivery with idempotent intake. A transport acknowledgement means durable peer acceptance, not successful product projection.
- **Projection tables** are read models only. They must not silently replace the source tables or grant write authority.
- **Object storage/CDN** owns binary transfer and delivery. The application API authorizes lifecycle transitions; private objects must never be exposed via public CDN paths.
- **Cloudflare, Firebase, Redis/Valkey, and Upstash** remain in place until each responsibility has an explicitly reviewed migration and rollback.

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

- Keep the current Supabase identity project and RLS behavior while routing is staged. Never use a service-role credential to impersonate a user or bypass authorization.
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
- [ ] XClone and backend CI pass on the exact commits proposed for rollout.
- [ ] Staging verifies Auth/RLS, rollback to the old route, dashboards, and alert delivery.
- [ ] Production migrations are backed up, reviewed, and applied separately; CI integration does not prove production migration success.

## Rollout rule

Migrate one capability family at a time. Record its source of truth, contract, authorization rule, metrics, regression tests, staging evidence, feature flag, and rollback. Do not enable global dual-write, replication workers, virtual storage, or DNS changes as a side effect of merging documentation or passing builds.
