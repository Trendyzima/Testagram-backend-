# Self-hosted Testagram Backend

Portable PostgreSQL-first base deployment for owned hardware or any Docker host.
The Data Centre services are deployed as a separate Compose overlay on stable
private networks created by this stack.

## Base services

- PostgreSQL 17: canonical product, identity bridge, media metadata, security counters and transactional outboxes.
- Testagram Go API: Testagram-native end-user authentication and PostgreSQL-backed sessions, canonical identity, social/search/media APIs and service orchestration.
- Control plane: node/workload inventory and hosting operations.
- S3-compatible object storage: private and public media buckets.
- Testagram-native staff console: native staff principals, server-side PostgreSQL role checks and verified WebAuthn MFA.
- Go CDN media delivery/cache, Caddy, Prometheus, Alertmanager and Grafana.

The base stack creates two stable networks for cross-repository services:
`testagram-platform` (private database/service traffic) and
`testagram-egress` (controlled outbound service traffic). The Data Centre
overlay owns the private Algorithm ranker, Federation protocol service,
platform API, video, notification and background worker services.

There is no Supabase, Redis/Valkey, Upstash, generic cache API or Redis queue
service. Rate limits are PostgreSQL counters; durable async work uses
service-owned PostgreSQL outboxes and idempotent workers.

## First deployment

1. Copy `.env.example` to `.env`.
2. Set strong unique values for PostgreSQL, MinIO, control-plane, media and admin session secrets.
3. Configure the Testagram-native identity and staff security settings. Set a unique `TESTAGRAM_AUTH_JWT_SECRET` (at least 32 characters), HTTPS `AUTH_PUBLIC_BASE_URL`, and working SMTP settings (`SMTP_HOST`, `SMTP_USERNAME`, `SMTP_PASSWORD`, `AUTH_FROM_EMAIL`). Configure secrets through protected VPS/GitHub secret stores; never commit them.
4. Use Testagram-native authentication only. There is no external identity-provider selector, shadow mode or fallback authentication path. Native activation requires the forward-only migrations, native account/session schema, a unique `TESTAGRAM_AUTH_JWT_SECRET`, HTTPS `AUTH_PUBLIC_BASE_URL` and working SMTP settings.
5. Do not configure Onspace AI or introduce an unapproved external moderation vendor. Keep publishing fail-closed unless the approved Testagram moderation implementation is deployed and its documented configuration, health checks and acceptance tests pass. Configure only secrets explicitly defined by the reviewed Testagram moderation implementation through protected deployment settings; never commit credentials.
6. Keep `TESTAGRAM_IDENTITY_REVIEW_WEBAUTHN_ENABLED=false` until the Admin Console-to-Backend WebAuthn elevation, two-person review, private evidence storage, VPS/R2/B2 replication and restore acceptance pass. Generate a random 32+ byte `TESTAGRAM_WEBAUTHN_READINESS_TOKEN` and place the same value in the API and Admin Console environment; do not commit it. Keep `TESTAGRAM_STAFF_WEBAUTHN_ENROLLMENT_ENABLED=false` until independent enrollment authorization and the staff enrollment ceremony are approved. Native registration is the sole application/review/provisioning workflow.
7. Confirm the native identity intake, review, staff WebAuthn, session/provisioning and runtime-grant migrations are included. For an existing PostgreSQL volume, use the explicit migration workflow; it creates and restore-tests a backup before applying the additive chain. Any historical account reconciliation is a separate, reviewed, one-time data operation; it must not create a runtime dependency on an external identity provider.
8. Configure DNS for `API_DOMAIN`, `MEDIA_DOMAIN` and `ADMIN_DOMAIN`, then set `ACME_EMAIL`.
   The Flutter client publishes a static web image to GHCR; the Backend deployment resolves `TESTAGRAM_WEB_IMAGE` to an immutable digest and serves it through the dedicated Caddy edge listener. Cloudflare routes `www.testagram.site` to that listener, which forwards the request to the private Flutter web container. The GHCR package must be publicly pullable by InstaBox (or use a least-privilege read-only registry credential). Keep Cloudflare Pages domain associations unchanged until the web-origin health and public-host smoke checks pass.
9. Review `VPS_ALLOWED_WORKLOAD_IMAGES`; an empty value intentionally disables workload scheduling.
10. Start the Backend base stack:

```sh
docker compose --env-file .env -f docker-compose.yml config
docker compose --env-file .env -f docker-compose.yml up -d --build
docker compose --env-file .env -f docker-compose.yml ps
```

8. Clone the Data Centre repository on the same host. Copy
   `backend/.env.example` to `backend/.env`; set `DATABASE_URL` to the same
   PostgreSQL user/password/database as this stack, use the Testagram-native service identity, and generate independent platform/notification/video
   secrets.
9. Start the Data Centre overlay. Its migration runner applies the
   Data Centre-owned schemas to the Backend-owned PostgreSQL database before
   the platform, video, notification and worker services start:

```sh
docker compose --env-file backend/.env -f backend/docker-compose.yml config
docker compose --env-file backend/.env -f backend/docker-compose.yml run --rm migrate
docker compose --env-file backend/.env -f backend/docker-compose.yml up -d --build
docker compose --env-file backend/.env -f backend/docker-compose.yml ps
```

The Backend Compose stack creates the external networks required by the
Data Centre overlay. Start it first. The overlay does not publish ports or run
a second reverse proxy; Caddy remains the only public edge.

The Backend PostgreSQL container applies Backend-owned migrations on first
initialization. Existing PostgreSQL volumes do not rerun Docker init scripts.
For an already-initialized database, apply the Federation outbox and Backend-owned remote-follow migrations
through the backup-verified operator script before enabling any Federation workers:

```sh
./apply-federation-outbox-migration.sh
```

The script waits for PostgreSQL, creates and validates a custom-format backup,
requires an explicit confirmation phrase, applies migrations 022 and 024, and verifies both the outbox and canonical remote-follow tables. Migration 024 makes Backend the canonical authority for remote Follow/Undo state; Federation's follower rows remain only a delivery-routing cache. Retain the backup until Backend and Federation runtime smoke tests pass. The Data Centre migration runner tracks its own applied files
and checksums in the same database. Never edit an already-applied migration;
add a new forward-only migration instead.

## Social platform product foundation migration

The additive social-platform foundation migration creates curated Lists, private
post drafts, scheduled-post commands, ephemeral Stories, safety reports,
moderation actions and appeals. It does not publish content or start a scheduler.
The API must continue enforcing native authentication and privacy; scheduled
publishing must use the same moderation and idempotency path as interactive
publishing.

For an existing PostgreSQL volume, Docker init scripts do not rerun. Apply this
migration only after reviewing the SQL and confirming a recent restorable backup:

```sh
./apply-social-platform-product-migration.sh
```

The operator script waits for PostgreSQL, creates a custom-format database
backup, verifies the archive and SHA-256 checksum, and requires the exact
`APPLY-TESTAGRAM-SOCIAL-PLATFORM-FOUNDATION` confirmation before applying
migration 063. It is idempotent when all expected tables already exist. Retain
the backup until the deployed API and Flutter acceptance tests pass. Do not
manually create the tables or bypass the backup/confirmation guard.

## Testagram Wallet and M-Pesa

The native wallet is KES-only at launch. It supports internal wallet transfers and Safaricom STK Push deposits. A top-up is credited only after the public callback is checked against Safaricom's STK query API and the Backend commits the wallet ledger entry.

For sandbox testing, set the following in the .env file:

- MPESA_ENV=sandbox
- MPESA_SHORTCODE, MPESA_PASSKEY, MPESA_CONSUMER_KEY and MPESA_CONSUMER_SECRET from the Safaricom Daraja sandbox application.
- MPESA_CALLBACK_URL=https://<your-api-domain>/api/wallet/mpesa/callback

The callback URL must be public HTTPS and registered with the matching Daraja application. The Compose service passes these values to the API container; never commit real credentials or put them in Flutter configuration. Switch MPESA_ENV=live only after the business shortcode, production credentials, callback registration, end-to-end settlement test and operational alerting have been verified. If credentials are absent or incomplete, the Backend fails closed with MPESA_NOT_CONFIGURED; it does not credit the wallet.

## Authentication and authorization

- Flutter uses Testagram-owned sign-in, refresh, logout and password-recovery pages. In native mode it sends short-lived Backend-issued access tokens; the rotating refresh credential is stored in platform secure storage.
- Testagram-native PostgreSQL sessions are the only end-user authentication mode. Invalid or expired native tokens fail closed; there is no external-provider fallback.
- Backend owns canonical account identity, account activation, authorization and product records. An imported legacy account remains blocked from native login until the audited import is complete and its required password reset has been completed.
- Staff authorization is Testagram-native: enabled staff principals, PostgreSQL role grants, independent two-person approval and backend-verified WebAuthn MFA. Browser metadata cannot grant staff privileges.
- Data Centre APIs are private service interfaces authenticated with service credentials; they must not become an alternative end-user identity authority.
- No Supabase JWT secret, PostgREST service key, or cross-provider fallback is accepted.

## Media lifecycle

1. The Backend creates a bounded upload intent in PostgreSQL.
2. The client uploads to the signed private/public object-storage boundary.
3. Completion validates ownership, size and checksum before marking the asset ready.
4. Attaching media writes the native object key into `testagram_local.posts.media_ids`.
5. Delete/quarantine writes a durable PostgreSQL deletion job before removing bytes.
6. The Data Centre video worker uses the same canonical database and its persistent media root for FFmpeg processing.

Do not manually delete object bytes or edit asset status rows outside the API/worker lifecycle.

## CDN media delivery

The `media-cache` Compose service is the Go CDN, pinned by immutable digest in
`TESTAGRAM_CDN_IMAGE`. It listens on private container port 8081 and joins only
the `edge` bridge shared with Caddy and Backend. It has no Docker host-port
mapping. The Instabox deployment workflow publishes that service through the
InstaBox port agent and updates `CDN_MEDIA_BASE_URL` and
`MEDIA_PUBLIC_BASE_URL` to the returned HTTPS endpoint.

- CDN origin: `http://api:8080`; the CDN cannot read MinIO, B2 or R2 directly.
- `CDN_ORIGIN_AUTH_TOKEN` authenticates CDN-to-Backend origin requests.
- `CDN_MEDIA_ASSERTION_SECRET` separately signs short-lived, viewer-bound
  private-media assertions. Never reuse the origin token as the assertion key.
- `CDN_PURGE_TOKEN` is a third independent credential for the private
  `POST /api/cache/purge` endpoint. The Backend API purge client/retry path is
  still a separate acceptance gate; provisioning this token alone does not
  make deletion or visibility revocation immediately invalidate cached bytes.
- Backend metadata is authoritative: only `ready` + `public` assets may be
  returned without a viewer assertion. Private responses require a valid
  viewer-bound assertion and use `private, no-store`; the CDN bypasses shared
  cache for them.
- MinIO signing remains on `http://object-storage:9000` inside the Docker
  network. The separately published MinIO endpoint is retained temporarily
  only for compatibility with previously issued direct-object URLs; new media
  URLs must use the Go CDN. Remove that legacy public mapping after stored URL
  migration is verified.
- Public CDN cache entries have a bounded shared-cache lifetime. A visibility
  change or deletion must also be covered by the Backend/CDN purge and
  tombstone acceptance tests before production activation.

Keep `FRESH_MEDIA_ENABLED=false` until the separate Fresh API media adapter,
Data Centre ledger reconciliation and real B2/R2 provider acceptance pass.

For the exact B2/R2 secret names, bucket separation requirements, deployment-path differences and manual credential smoke test, see [Media provider credential setup](MEDIA-PROVIDER-SETUP.md). Provider credentials are Backend-owned; do not copy them into the Data Centre repository.

## Backup and restore

- Back up PostgreSQL with a consistent snapshot and test restores regularly.
- Back up MinIO/object storage and the Data Centre video media root; preserve checksums and namespace metadata.
- Back up Caddy data/config, Grafana/Prometheus data where required, and all deployment secrets.
- Preserve `ADMIN_SESSION_KEY`, `TESTAGRAM_AUTH_JWT_SECRET`, SMTP credentials and native staff WebAuthn recovery/operational secrets securely. No external identity-provider endpoint, project identifier, API key or provider-issued JWT is required.
- No Redis/Valkey queue state exists to back up; transactional outbox rows are in PostgreSQL.

## Network and operations

- The InstaBox port agent publishes the Backend public listener on port 8080 and the Go CDN public listener on port 8081. Both services use a separate Docker-network-only internal listener on port 8082 for readiness and metrics; never publish either internal listener through the port agent or a host port.
- Keep PostgreSQL, object storage administration, Prometheus, Grafana and internal APIs private.
- Public `/healthz` is liveness only. Backend `/readyz`, `/metrics` and Federation-to-Backend internal routes are served only on `api:8082`; Docker healthchecks, Admin Console, Prometheus and Federation use that private listener. Never probe these endpoints through the public API URL.
- Alert on PostgreSQL saturation, oldest outbox age, failed media deletions, worker lag and storage capacity.
- Test restore and service recovery before production cutover.

## Rollback

Roll back the application image only after confirming the database migration is
backward-compatible. The native media-link migration removes the old external
media ID column; older application versions that require that column cannot be
rolled back safely after it is applied. Use a forward fix or restore a verified
pre-migration backup instead.


The Backend `FEDERATION_PUBLIC_BASE_URL` must exactly match the Federation service `PUBLIC_BASE_URL`. The Backend uses this origin for ActivityPub actor, activity and public object IDs; a mismatch causes signed activity enqueue authorization to fail closed.
