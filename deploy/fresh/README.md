# Fresh Appwrite API deployment

This deployment is intentionally separate from the legacy PostgreSQL/Supabase API. Build the new binary with the repository's Dockerfile.fresh and run only this service for the fresh environment.

1. Create a new Appwrite project and database; do not reuse a legacy project or Supabase identifiers.
2. Create the collections and indexes described in docs/fresh-appwrite-database-schema.md.
3. Create a server API key scoped only to the required database document read/write operations. Keep it in the deployment secret store.
4. Copy .env.example to the deployment environment and fill the Appwrite values. Do not commit secrets.
5. Build: `docker build -f Dockerfile.fresh -t testagram-fresh-api:dev .`
6. Run with the fresh environment file and expose port 8081 only behind the HTTPS Cloudflare-protected API origin.
7. Confirm /healthz and /readyz, then test Appwrite signup/login and GET/PUT /v1/me/profile with a real staging Appwrite JWT.

The fresh API implements health/readiness, authenticated profile read/write, and an opt-in private media upload lifecycle. Set FRESH_MEDIA_ENABLED=true only after provisioning all media collections, attributes and indexes described in the schema. The media API requires both Backblaze B2 and Cloudflare R2 credentials and fails startup if either provider is incomplete. Set `FRESH_MEDIA_PRIMARY_PROVIDER=b2` or `r2` to choose which provider receives new direct browser uploads; the chosen provider is persisted on each reservation, while the other provider receives a verified full replica. Existing reservations without this field remain B2-origin for backwards compatibility.


## Appwrite identity + Neon PostgreSQL data plane

Appwrite remains the identity provider: the browser/mobile client signs in with Appwrite, sends a short-lived Appwrite JWT to Backend, and Backend validates it against Appwrite. Appwrite also currently stores the fresh profile, legal-consent, feed, media-reservation, quota and replica-ledger documents.

Neon is connected in the fresh API through the bounded `internal/neondb` pgx pool. Set `NEON_DATABASE_URL` to the Neon pooled connection string (TLS required) and `NEON_REQUIRED=true` so `/readyz/platform` fails closed when Neon is unavailable. The additive Neon schema is in `migrations/neon/001_social_data_plane.sql` and `migrations/neon/002_federation_outbox.sql`; apply those migrations to the dedicated Neon database using a migration-capable operator account before enabling Neon-dependent features. Use a separate least-privilege runtime role and never put the URL in frontend variables.

**Important integration boundary:** the current fresh API uses Neon for connectivity/readiness only; its profile, post and home-feed CRUD still uses Appwrite DocumentsDB. The canonical Neon social repositories and transactional write/read cutover are not yet complete, so do not treat Neon as the live source of truth for posts yet. Federation has its own Neon-backed inbox/outbox store in the federated-content repository. The intended division is Appwrite for authentication/identity and Neon for canonical social/product data, with media bytes in B2 + R2. Keep `/readyz/platform` as the production gate until the Backend social repositories, migrations, outbox handoff and end-to-end consistency tests are complete.

## Dual object storage: B2 + Cloudflare R2

Cloudflare R2 is a real object-storage provider in this architecture. It stores the actual image/video/audio/file bytes in a private R2 bucket, just as Backblaze B2 stores a complete copy. Cloudflare Workers/CDN provide a delivery and authorization layer in front of R2; CDN cache is not counted as a storage replica.

- POST /v1/media/uploads/reservations — requires an Appwrite session, an Idempotency-Key header, exact byte count and MIME type. The client may optionally provide SHA-256; otherwise Backend calculates it from the selected primary object.
- Upload directly to the returned short-lived presigned PUT URL for the selected primary provider using the returned Content-Type header.
- POST /v1/media/uploads/{reservationID}/complete — verifies primary-provider HEAD metadata and streams the actual primary object to calculate SHA-256; copies the bytes to the other private bucket with bounded streaming and independently verifies the replica read-back checksum. Only then does Backend commit the media and two-provider replica ledger.
- Both B2 and R2 retain the full object. The media object is not marked protected/ready until both copies pass verification.
- GET /v1/media/{objectID}/download — requires the owning Appwrite session and returns a two-minute private URL. When CLOUDFLARE_MEDIA_BASE_URL and CLOUDFLARE_MEDIA_SIGNING_SECRET are configured, the URL goes through the Cloudflare private-media Worker, which reads the bytes from its R2 binding. Otherwise Backend falls back to a direct two-minute R2 presigned URL.
- Repair verifies both replicas against the canonical SHA-256 and can restore either missing/corrupt copy from the surviving verified provider. CDN caching is not a substitute for this replica process.

Configure separate private buckets and credentials:

- B2: B2_S3_ENDPOINT, B2_S3_REGION, B2_S3_BUCKET, B2_S3_ACCESS_KEY_ID, B2_S3_SECRET_ACCESS_KEY
- R2: CLOUDFLARE_R2_ENDPOINT, CLOUDFLARE_R2_REGION, CLOUDFLARE_R2_BUCKET, CLOUDFLARE_R2_ACCESS_KEY_ID, CLOUDFLARE_R2_SECRET_ACCESS_KEY
- Worker: CLOUDFLARE_MEDIA_BASE_URL and CLOUDFLARE_MEDIA_SIGNING_SECRET

The B2/R2 dual-origin lifecycle, persisted reservation origin, dual-copy verification and bidirectional repair path are implemented. Run the Backend CI workflow before enabling this in a live environment. Run go run ./cmd/fresh-storage-expire from a trusted scheduler; it releases quota only after both provider objects are confirmed absent. For operator-triggered repair, set TESTAGRAM_REPAIR_OBJECT_ID and run go run ./cmd/fresh-storage-repair. The Cloudflare private-media Worker and matching Go signer are implemented but not deployed; configure its R2 binding, route, frontend origins and shared signing secret before enabling CDN delivery.

Scheduled replica repair, Android/computer node object transport, public-media caching and wiring the frontend upload service into post/message/profile screens are not yet production complete. Do not route production frontend traffic to this API until remaining integration and live staging tests pass.

### Browser and object-store CORS

Set FRESH_API_ALLOWED_ORIGINS to exact frontend origins (comma-separated, no paths or wildcards), for example https://app.example.com. The API rejects unlisted cross-origin requests and only permits required Authorization, Content-Type and Idempotency-Key headers.

Because browser uploads go directly to the selected primary provider, configure bucket CORS on both B2 and R2 to allow the exact frontend origin(s), method PUT, and request header Content-Type. The unselected provider receives its replica server-to-server. Do not expose either provider's credentials to browsers.

### Retention workers (not activated)

The Backend now contains a one-job dispatcher at `cmd/fresh-retention-worker`. A trusted scheduler must inject `TESTAGRAM_RETENTION_JOB_ID` and `TESTAGRAM_RETENTION_WORKER_ID`; the job must already exist in Appwrite `retention_jobs`. The dispatcher renews a 10-minute Appwrite lease every 2 minutes during work. It refuses to start unless `TESTAGRAM_RETENTION_ENABLED=true`, `TESTAGRAM_RETENTION_DRY_RUN=false`, and `TESTAGRAM_RETENTION_START_AT` is set. Keep these settings disabled until the candidate scanner, live Appwrite/B2/R2/archive tests, user-deletion workflow, node replica deletion, and operational retry/kill-switch controls are complete. Do not invoke this worker in production yet.

Archive configuration is separate from active B2/R2: set all `ARCHIVE_S3_*` values to an independent cold-archive provider and bucket before archive jobs can run. The archive worker verifies size, content type and SHA-256 read-back before it records the archive replica; it does not remove active replicas.

### Retention candidate scanner

Run `go run ./cmd/fresh-retention-scan` against the fresh Appwrite database to produce a JSON report. Set `TESTAGRAM_RETENTION_START_AT` to the approved RFC3339 rollout cutoff first. With the default disabled + dry-run configuration, the scanner only reads Appwrite records and reports eligible action counts and logical bytes; it does not create jobs or mutate media. For more than one page, pass the returned created/deleted offsets as `TESTAGRAM_RETENTION_CREATED_OFFSET` and `TESTAGRAM_RETENTION_DELETED_OFFSET` on the next invocation. Reference counts and live provider checks still require staging integration.
