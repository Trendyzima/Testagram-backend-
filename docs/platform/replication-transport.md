# Replication transport deployment

This transport is opt-in. It is not a product-data projector and does not make the
two backends fully synchronized by itself.

## Testagram environment

Configure both sides with a unique origin string and a shared secret of at least
32 random bytes (use a secret manager; never commit it).

- `REPLICATION_ORIGIN=testagram-vps`
- `REPLICATION_PEER_ORIGIN=xclone`
- `REPLICATION_PEER_KEY=<shared-random-secret>`
- `REPLICATION_PEER_URL=https://<xclone-project>.supabase.co/functions/v1/replication-receiver` (enables outbound relay)

The API exposes `POST /internal/replication/events` only when peer origin/key are
configured. It validates HMAC-SHA256 over `timestamp + "." + exact request bytes`,
requires a timestamp within five minutes, caps the body at 1 MiB, and stores
accepted events in `platform_replication_inbox`. The relay requires an HTTPS URL
and is disabled if `REPLICATION_PEER_URL` is absent.

For XClone's receiver, configure `REPLICATION_PEER_KEYS_JSON` as a JSON object
mapping the Testagram origin to the same shared secret. Configure its relay with
`REPLICATION_ORIGIN=xclone`, `REPLICATION_PEER_URL=https://<testagram-host>/internal/replication/events`,
`REPLICATION_PEER_KEY`, and a separate `REPLICATION_RELAY_TOKEN`.

## Important delivery semantics

A 2xx from the receiver means **durably accepted into the inbox**, not applied to
product tables. Do not claim content parity until a domain projector consumes
pending inbox events, checks entity versions and privacy rules, and commits each
projection atomically. The current implementation deliberately does not invent
mappings between unlike schemas.

## Safe rollout

1. Back up both databases and verify a restore.
2. Apply each repository's additive migration through its normal migration pipeline.
3. Configure peer secrets independently in each deployment.
4. Test HMAC rejection, timestamp expiry, duplicate delivery, conflicting event IDs,
   unavailable peers, retry after recovery, and payload-size limits.
5. Start with non-sensitive test entities only. Keep real product event producers
   disabled until projection handlers and reconciliation are implemented.
6. Schedule XClone's relay function through an operator-controlled scheduler or
   Supabase Cron after validating the environment; the function is not scheduled
   automatically by this change.

Never put private post bodies, credentials, signed media URLs, or bearer tokens in
events. Never use a Redis cache as the durable outbox.


## Inbox projection worker (opt-in)

Set `REPLICATION_APPLY_ENABLED=true` only after applying the additive replication
migration and reviewing the projection policy. The worker polls the durable inbox
every five seconds and processes at most 25 events per pass. It writes to
`platform_replication_projection`, a separate public-data mirror table; it does
not mutate the authoritative account, post, or media tables.

The built-in projector supports only `account`, `post`, and `media` entity
types. It uses per-type field allowlists, rejects email/phone/session/credential
fields, requires explicitly public posts and media, and requires media checksum
metadata. Unsupported or invalid projection payloads are marked rejected instead
of retrying forever. Transient database errors remain retryable with backoff.

This is intentionally a safe mirror rather than automatic product-table parity.
Do not enable production event producers until each product write is connected
to an atomic outbox event, each destination projector is mapped to its actual
domain schema, and cross-account/privacy tests pass.
