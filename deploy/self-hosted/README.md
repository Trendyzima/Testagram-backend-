# Self-hosted Testagram Backend

Portable PostgreSQL-first base deployment for owned hardware or any Docker host.
The Data Centre services are deployed as a separate Compose overlay on stable
private networks created by this stack.

## Base services

- PostgreSQL 17: canonical product, identity bridge, media metadata, security counters and transactional outboxes.
- Testagram Go API: Appwrite JWT verification, social/search/media APIs and service orchestration.
- Control plane: node/workload inventory and hosting operations.
- S3-compatible object storage: private and public media buckets.
- Appwrite-authenticated staff console: server-side PostgreSQL role checks.
- Caddy, Prometheus, Alertmanager and Grafana.

The base stack creates two stable networks for cross-repository services:
`testagram-platform` (private database/service traffic) and
`testagram-egress` (controlled outbound Appwrite access). The Data Centre
overlay owns the platform API, video, notification and background worker
services.

There is no Supabase, Redis/Valkey, Upstash, generic cache API or Redis queue
service. Rate limits are PostgreSQL counters; durable async work uses
service-owned PostgreSQL outboxes and idempotent workers.

## First deployment

1. Copy `.env.example` to `.env`.
2. Set strong unique values for PostgreSQL, MinIO, control-plane, media and admin session secrets.
3. Set `APPWRITE_ENDPOINT` and `APPWRITE_PROJECT_ID`. Set `APPWRITE_API_KEY` only if owners will issue staff invitations; restrict it to user-management operations.
4. Confirm the Appwrite identity bridge, native media-link and staff-role migrations are included.
5. Configure DNS for `API_DOMAIN`, `MEDIA_DOMAIN` and `ADMIN_DOMAIN`, then set `ACME_EMAIL`.
6. Review `VPS_ALLOWED_WORKLOAD_IMAGES`; an empty value intentionally disables workload scheduling.
7. Start the Backend base stack:

```sh
docker compose --env-file .env -f docker-compose.yml config
docker compose --env-file .env -f docker-compose.yml up -d --build
docker compose --env-file .env -f docker-compose.yml ps
```

8. Clone the Data Centre repository on the same host. Copy
   `backend/.env.example` to `backend/.env`; set `DATABASE_URL` to the same
   PostgreSQL user/password/database as this stack, use the same Appwrite
   endpoint/project ID, and generate independent platform/notification/video
   secrets.
9. Start the Data Centre overlay:

```sh
docker compose --env-file backend/.env -f backend/docker-compose.yml config
docker compose --env-file backend/.env -f backend/docker-compose.yml up -d --build
docker compose --env-file backend/.env -f backend/docker-compose.yml ps
```

The Backend Compose stack creates the external networks required by the
Data Centre overlay. Start it first. The overlay does not publish ports or run
a second reverse proxy; Caddy remains the only public edge.

The PostgreSQL container applies the ordered SQL migrations on first
initialization. For an existing database, apply each new migration with
`psql -v ON_ERROR_STOP=1` before deploying the matching application commit.

## Authentication and authorization

- Flutter obtains an Appwrite session and sends its JWT to the Backend.
- The Backend validates the JWT with Appwrite and resolves the canonical Testagram UUID in PostgreSQL.
- Data Centre video and notification services use the same Appwrite validation and identity bridge.
- The Data Centre platform API uses PostgreSQL and a private internal token.
- The staff console validates Appwrite sessions and reads roles from
  `testagram_auth.staff_roles`; browser metadata cannot grant staff privileges.
- No Supabase JWT secret, PostgREST service key, or provider fallback is accepted.

## Media lifecycle

1. The Backend creates a bounded upload intent in PostgreSQL.
2. The client uploads to the signed private/public object-storage boundary.
3. Completion validates ownership, size and checksum before marking the asset ready.
4. Attaching media writes the native object key into `testagram_local.posts.media_ids`.
5. Delete/quarantine writes a durable PostgreSQL deletion job before removing bytes.
6. The Data Centre video worker uses the same canonical database and its persistent media root for FFmpeg processing.

Do not manually delete object bytes or edit asset status rows outside the API/worker lifecycle.

## Backup and restore

- Back up PostgreSQL with a consistent snapshot and test restores regularly.
- Back up MinIO/object storage and the Data Centre video media root; preserve checksums and namespace metadata.
- Back up Caddy data/config, Grafana/Prometheus data where required, and all deployment secrets.
- Preserve `ADMIN_SESSION_KEY`, Appwrite project configuration and server-only Appwrite API key securely.
- No Redis/Valkey queue state exists to back up; transactional outbox rows are in PostgreSQL.

## Network and operations

- Publish only Caddy ports 80/443 and explicitly approved control-plane endpoints.
- Keep PostgreSQL, object storage administration, Prometheus, Grafana and internal APIs private.
- `/readyz` checks required PostgreSQL dependencies; `/metrics` exposes API, database and outbox signals.
- Alert on PostgreSQL saturation, oldest outbox age, failed media deletions, worker lag and storage capacity.
- Test restore and service recovery before production cutover.

## Rollback

Roll back the application image only after confirming the database migration is
backward-compatible. The native media-link migration removes the old external
media ID column; older application versions that require that column cannot be
rolled back safely after it is applied. Use a forward fix or restore a verified
pre-migration backup instead.
