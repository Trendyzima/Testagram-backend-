# XClone client integration contract for Virtual Storage

This repository owns the self-hosted Testagram API and the logical storage lifecycle. The XClone client integration contract is maintained in the companion repository at:
https://github.com/Trendyzima/XClone-9b4dz1-9b4dz1-8936-20260809tr/blob/feat/testagram-virtual-storage-contract/docs/virtual-storage-integration-contract.md

## Non-negotiable data-preservation rule

Existing Supabase product rows and legacy media keys/URLs remain authoritative. Do not bulk-copy, rename, or delete existing media during rollout. New virtual storage is opt-in; `VIRTUAL_STORAGE_ENABLED` stays false until all deployment and live lifecycle gates pass. Legacy rows marked `legacy-media` must continue to use their existing provider path.

## Service responsibilities

- Testagram Go API: JWT validation, owner/tenant scoping, upload intents, atomic single-use claims, bounded streaming, SHA-256 verification, quota reservations, lifecycle state, private retrieval and durable deletion retries.
- Supabase Auth/Postgres: canonical identity and durable product/media metadata. Schema changes are additive and applied explicitly to existing volumes after verified backup.
- MinIO on the VPS: default self-hosted S3-compatible object store; keep its API/console private behind the internal network.
- Cloudflare R2: optional S3-compatible provider, not an implicit replacement for local MinIO or legacy objects.
- Cloudflare edge/CDN: public media delivery and edge controls. Private objects must bypass shared cache and never be routed through public bucket access.
- Redis/Valkey/Upstash: cache, rate limiting and queue acceleration; not durable ownership, quota or lifecycle truth.

## Client/API compatibility

The client expects these authenticated routes:

- `POST /api/media/upload-intent`
- `PUT /api/media/upload?key=<intent-key>` with raw bytes and the exact intent `Content-Type`
- `POST /api/media/complete` with `key`, `visibility`, `content_type`, and `checksum_sha256`
- `POST /api/media/attach`
- `POST /api/media/private-download-url`
- `POST /api/media/delete`

The upload intent URL is a relative API-gateway path, not a direct R2/MinIO URL. The client must resolve it against `VITE_TESTAGRAM_API_URL`, validate same-origin/exact-path/key, and never send a bearer token to a different origin. The gateway must atomically claim an intent before consuming the body; exactly one concurrent/replayed PUT may succeed. Completion must hash the actual stored bytes; ETag must never be treated as SHA-256.

The current client contract caps public media at 20 MiB. Raise the cap only when API, proxy body limits, spool-disk capacity/cleanup, hashing strategy, provider policy and integration tests are raised together.

## VPS environment and migration safety

- Set `VIRTUAL_STORAGE_ENABLED=false` unless the full release gate is approved.
- Keep provider credentials server-only and use separate public/private buckets.
- Configure the public URL base and CDN origin path to preserve tenant-prefixed object keys.
- Apply migrations to existing PostgreSQL volumes explicitly after a verified backup. Compose/Postgres init scripts only initialize fresh data directories.
- Do not rely on Redis/Upstash for correctness during outage or failover.
- Never expose MinIO ports, internal platform/control-plane ports, Grafana, Prometheus or privileged tokens publicly.

## Release gates

1. Run Go unit/race tests, vet/build, migration idempotency tests, full CI, Compose validation and S3 lifecycle integration tests on the exact candidate commit.
2. Test on a restored staging database with actual MinIO; separately smoke-test R2 if configured.
3. Verify upload size/type/checksum, intent replay/expiry, cross-account denial, attachment ownership, private-cache bypass, delete retries, janitor cleanup and concurrent quota reservations.
4. Verify Cloudflare CORS, public origin path mapping and private-media isolation through the real hostname.
5. Exercise API/Redis/provider outage behavior and confirm durable metadata remains correct.
6. Restore an off-host backup into an isolated environment and test login, post media, private media and playback. Record observed RPO/RTO.
7. Verify Prometheus scrape targets and storage/API dashboards; send a test alert through the configured Alertmanager receiver and confirm receipt.

## Rollout and rollback

Keep legacy media routes and the hosted compatibility path working. Enable virtual storage only for a staged deployment after every gate passes; expand traffic gradually while monitoring errors, disk pressure, quota failures, deletion backlog and cache behavior. Roll back by disabling the flag and reverting client routing, not by deleting objects or rewriting legacy rows.

This contract does not claim the live VPS, R2, Cloudflare CDN mapping, notification delivery, or backup restoration has been tested from this repository automation. Those require operator credentials and controlled staging access.
