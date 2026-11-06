# Fresh Appwrite API deployment

This deployment is intentionally separate from the legacy PostgreSQL/Supabase API. Build the new binary with the repository's Dockerfile.fresh and run only this service for the fresh environment.

1. Create a new Appwrite project and database; do not reuse a legacy project or Supabase identifiers.
2. Create the collections and indexes described in docs/fresh-appwrite-database-schema.md.
3. Create a server API key scoped only to the required database document read/write operations. Keep it in the deployment secret store.
4. Copy .env.example to the deployment environment and fill the Appwrite values. Do not commit secrets.
5. Build: docker build -f Dockerfile.fresh -t testagram-fresh-api:dev .
6. Run with the fresh environment file and expose port 8081 only behind the HTTPS Cloudflare-protected API origin.
7. Confirm /healthz and /readyz, then test Appwrite signup/login and GET/PUT /v1/me/profile with a real staging Appwrite JWT.

The initial fresh API currently implements health/readiness and a minimal authenticated profile read/write vertical slice. Media upload, social graph, feed, messaging and notifications are not yet implemented in this fresh API. Do not route production frontend traffic to it until those required product endpoints and security tests are complete.

Backblaze B2 and Cloudflare R2 credentials are prepared as server-only configuration inputs. The fresh API does not yet expose media routes or activate object placement; do not claim B2/R2 media is live until upload, checksum, private retrieval, deletion, quota and restore tests pass.
