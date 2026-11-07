# Fresh Appwrite API deployment

This deployment is intentionally separate from the legacy PostgreSQL/Supabase API. Build the new binary with the repository's Dockerfile.fresh and run only this service for the fresh environment.

1. Create a new Appwrite project and database; do not reuse a legacy project or Supabase identifiers.
2. Create the collections and indexes described in docs/fresh-appwrite-database-schema.md.
3. Create a server API key scoped only to the required database document read/write operations. Keep it in the deployment secret store.
4. Copy .env.example to the deployment environment and fill the Appwrite values. Do not commit secrets.
5. Build: docker build -f Dockerfile.fresh -t testagram-fresh-api:dev .
6. Run with the fresh environment file and expose port 8081 only behind the HTTPS Cloudflare-protected API origin.
7. Confirm /healthz and /readyz, then test Appwrite signup/login and GET/PUT /v1/me/profile with a real staging Appwrite JWT.

The fresh API implements health/readiness, authenticated profile read/write, and an opt-in private media upload lifecycle. Set FRESH_MEDIA_ENABLED=true only after provisioning all media collections, attributes and indexes described in the schema. The media API requires both B2 and R2 credentials and fails startup if either provider is incomplete.

### Fresh media API

- POST /v1/media/uploads/reservations — requires an Appwrite session, an Idempotency-Key header, exact byte count and MIME type. The client may optionally provide SHA-256; otherwise the backend calculates it from the uploaded B2 object. Appwrite transactions atomically reserve platform and user quota in KiB.
- Upload directly to the returned B2 presigned PUT URL using the returned Content-Type header.
- POST /v1/media/uploads/{reservationID}/complete — verifies B2 HEAD metadata and streams the actual object to calculate SHA-256; copies it to R2 with bounded streaming and independently verifies the R2 read-back checksum; only then commits the media and two-replica ledger and converts reserved quota to used quota.
- GET /v1/media/{objectID}/download — requires the owning Appwrite session and returns a two-minute private URL. When CLOUDFLARE_MEDIA_BASE_URL and CLOUDFLARE_MEDIA_SIGNING_SECRET are configured, the URL goes through the Cloudflare private-media Worker and R2 binding; otherwise it falls back to a direct two-minute R2 presigned URL. New media is private by default and responses are non-cacheable.

Before enabling, create the storage_quotas collection with integer attributes limit_kib, used_kib and reserved_kib. At startup the fresh API creates storage_quotas/platform with limit_kib=146484375, used_kib=0 and reserved_kib=0 if it does not exist; it never resets an existing quota. On first upload, it creates storage_quotas/<AppwriteUserID> with a 2 GiB default quota (configurable through TESTAGRAM_STORAGE_PER_USER_BYTES). The API key is the only actor allowed to create these documents.

The B2-to-R2 lifecycle and reservation expiry state machine are implemented and locally tested. Run go run ./cmd/fresh-storage-expire from a trusted scheduler; it releases quota only after both provider objects are confirmed absent. For operator-triggered repair, set TESTAGRAM_REPAIR_OBJECT_ID and run go run ./cmd/fresh-storage-repair. It repairs one cloud copy from the other only when the surviving source passes size, MIME and SHA-256 checks. The Cloudflare private-media Worker and matching Go signer are implemented but not deployed; configure its R2 binding, route, frontend origins and shared signing secret before enabling CDN delivery. Scheduled replica repair, Android/computer node object transport, public-media caching and wiring the frontend upload service into post/message/profile screens are not yet production complete. Do not route production frontend traffic to this API until the remaining integration and live staging tests pass.

### Browser and object-store CORS

Set FRESH_API_ALLOWED_ORIGINS to exact frontend origins (comma-separated, no paths or wildcards), for example https://app.example.com. The API rejects unlisted cross-origin requests and only permits the required Authorization, Content-Type and Idempotency-Key headers.

Because browser uploads go directly to B2, configure the B2 bucket CORS policy to allow the same frontend origin(s), method PUT, and request header Content-Type. Do not allow wildcard origins for private media. R2 replication and private downloads are performed by the Backend using server credentials and do not require browser access to R2.


### Retention workers (not activated)

The Backend now contains a one-job dispatcher at `cmd/fresh-retention-worker`. A trusted scheduler must inject `TESTAGRAM_RETENTION_JOB_ID` and `TESTAGRAM_RETENTION_WORKER_ID`; the job must already exist in Appwrite `retention_jobs`. The dispatcher renews a 10-minute Appwrite lease every 2 minutes during work. It refuses to start unless `TESTAGRAM_RETENTION_ENABLED=true`, `TESTAGRAM_RETENTION_DRY_RUN=false`, and `TESTAGRAM_RETENTION_START_AT` is set. Keep these settings disabled until the candidate scanner, live Appwrite/B2/R2/archive tests, user-deletion workflow, node replica deletion, and operational retry/kill-switch controls are complete. Do not invoke this worker in production yet.

Archive configuration is separate from active B2/R2: set all `ARCHIVE_S3_*` values to an independent cold-archive provider and bucket before archive jobs can run. The archive worker verifies size, content type and SHA-256 read-back before it records the archive replica; it does not remove active replicas.
