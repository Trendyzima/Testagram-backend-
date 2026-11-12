# Media provider credential setup

This guide covers the standard self-hosted Backend media path. Provider credentials belong to the Backend API runtime, not Flutter, Android/Linux nodes, or the Data Centre control-plane.

## Choose the deployment path first

There are two distinct implementations in this repository:

1. **Standard self-hosted API** (\`cmd/api\` + \`deploy/self-hosted/docker-compose.yml\`): Instabox VPS/MinIO is the primary store; Cloudflare R2 and Backblaze B2 are independent replicas. This path uses the provider variables below and is controlled by \`VIRTUAL_STORAGE_ENABLED\` and \`MEDIA_REPLICATION_ENABLED\`.
2. **Fresh Appwrite preview** (\`cmd/fresh-api\` + \`vps/docker-compose.fresh.yml\`): B2 and R2 are configured as a separate provider set using \`B2_S3_BUCKET\` and \`CLOUDFLARE_R2_BUCKET\`. It is a separate deployment path; do not mix its single-bucket names with the standard API's private/public bucket mappings. Keep \`FRESH_MEDIA_ENABLED=false\` until its own release gates pass.

The Data Centre coordinates node placement and operations. It must not receive the long-lived B2/R2 provider keys.

## Standard self-hosted API: GitHub Actions secrets

Store the following in the **Backend** repository's Actions secrets if a GitHub Actions workflow needs them, and in the Backend runtime secret store for production runtime use.

| Secret | Purpose |
|---|---|
| \`B2_S3_ACCESS_KEY_ID\` | Backblaze application key ID |
| \`B2_S3_SECRET_ACCESS_KEY\` | Backblaze application key |
| \`CLOUDFLARE_R2_ACCESS_KEY_ID\` | R2 S3 access key ID |
| \`CLOUDFLARE_R2_SECRET_ACCESS_KEY\` | R2 S3 secret access key |

Do not place these in the Data Centre repository, Flutter build variables, mobile apps, public CI logs, or source files.

## Standard self-hosted API: Actions variables / runtime configuration

| Variable | Required value |
|---|---|
| \`B2_S3_ENDPOINT\` | \`https://s3.us-east-005.backblazeb2.com\` for the current B2 bucket region |
| \`B2_S3_REGION\` | \`us-east-005\` |
| \`B2_S3_PRIVATE_BUCKET\` | Existing private bucket: \`audiostrendy\` |
| \`B2_S3_PUBLIC_BUCKET\` | A second, separately named B2 bucket; keep it private |
| \`CLOUDFLARE_R2_ENDPOINT\` | \`https://<account-id>.r2.cloudflarestorage.com\` |
| \`CLOUDFLARE_R2_REGION\` | \`auto\` |
| \`CLOUDFLARE_R2_PRIVATE_BUCKET\` | Dedicated private-media R2 bucket |
| \`CLOUDFLARE_R2_PUBLIC_BUCKET\` | Separate public-media namespace R2 bucket; keep it private |

The standard API intentionally rejects identical private and public bucket names for a provider. Do not set both B2 bucket variables to \`audiostrendy\`.

The Instabox VPS/MinIO primary is configured independently:

- \`MEDIA_S3_ENDPOINT\` (or the documented legacy MinIO endpoint)
- \`MEDIA_S3_ACCESS_KEY\` and \`MEDIA_S3_SECRET_KEY\` (or the documented legacy MinIO credentials)
- \`MEDIA_S3_REGION\`
- \`MEDIA_PRIVATE_BUCKET\` and \`MEDIA_PUBLIC_BUCKET\`
- \`MEDIA_S3_SIGNING_HOST\` when the public signing URL is served through a trusted reverse proxy

The standard API's replication startup checks require a working primary provider, both R2 and B2 credentials, both bucket mappings per provider, and the replica-ledger/outbox migrations.

## Backblaze application key scope

Create a dedicated non-master application key for Testagram. Restrict it to the two B2 bucket IDs used by the standard API (multi-bucket application keys are supported by Backblaze's current API). Grant only the file capabilities needed by the application: read, write and list; retain delete capability only if the selected B2 API operation requires version-specific deletion. Never grant key-management capabilities such as \`writeKeys\`.

The standard replication code uses S3-compatible object-name operations. Test the exact permission set against the actual bucket pair before enabling the worker. Do not use the master application key.

## Manual credential smoke test

The workflow \`.github/workflows/media-provider-smoke.yml\` is a manual, non-production-data test. It writes a uniquely named small test object to each configured B2 and R2 bucket, checks object size, reads it back, compares SHA-256 and verifies that the object is no longer visible after deletion. It never prints credential values.

Before running it, configure the secrets and variables above in the **Backend** repository. The test requires write/read/delete-by-name access to all four buckets. B2 buckets configured to keep all versions may retain the prior object version after the delete marker hides it; the test object is small and uses a unique CI prefix.

Run it from GitHub Actions → **Media provider credential smoke test** → **Run workflow**. Do not treat a successful credential smoke test as proof that the full application replication lifecycle, failover or restore has passed.

## Safe activation order

1. Keep \`MEDIA_REPLICATION_ENABLED=false\` and \`FRESH_MEDIA_ENABLED=false\`.
2. Configure and smoke-test the Instabox VPS/MinIO primary, B2 and R2 independently.
3. Apply and verify the forward-only media replica ledger and outbox migrations on the canonical Backend PostgreSQL database.
4. Run application-level upload, primary SHA-256 verification, R2/B2 streaming copy, destination read-back, retry, lease-expiry, dead-letter and reconciliation tests.
5. Test provider outage and corruption scenarios. A media object must not be marked fully replicated until all three required stores are verified.
6. Perform an independent backup and restore drill, including object bytes and private-media authorization.
7. Only then enable \`MEDIA_REPLICATION_ENABLED=true\` in the Backend runtime and monitor replica health, outbox age, retries and dead letters.

Retention and destructive lifecycle policies remain disabled/dry-run until a separately approved restore and deletion test passes.

## Important distinction

The fresh Appwrite preview uses \`B2_S3_BUCKET\` and \`CLOUDFLARE_R2_BUCKET\` (single bucket per provider) in its own \`freshstorage.FromEnv\` configuration. The standard self-hosted replication worker uses \`B2_S3_PRIVATE_BUCKET\`, \`B2_S3_PUBLIC_BUCKET\`, \`CLOUDFLARE_R2_PRIVATE_BUCKET\` and \`CLOUDFLARE_R2_PUBLIC_BUCKET\`. Keep the two contracts separate.
