# Identity evidence provider staging acceptance

This procedure exercises the real three-provider identity-evidence lifecycle. It is intentionally separate from ordinary CI and must never use production buckets or production credentials.

## Required GitHub environment

Create the GitHub Actions environment named `identity-evidence-staging`. Store the following as environment variables (not secrets):

- `IDENTITY_SMOKE_PRIMARY_ENDPOINT`
- `IDENTITY_SMOKE_PRIMARY_REGION`
- `IDENTITY_SMOKE_PRIMARY_PRIVATE_BUCKET`
- `IDENTITY_SMOKE_PRIMARY_PUBLIC_BUCKET`
- `IDENTITY_SMOKE_PRIMARY_IDENTITY_BUCKET`
- `IDENTITY_SMOKE_R2_ENDPOINT`
- `IDENTITY_SMOKE_R2_REGION`
- `IDENTITY_SMOKE_R2_PRIVATE_BUCKET`
- `IDENTITY_SMOKE_R2_PUBLIC_BUCKET`
- `IDENTITY_SMOKE_R2_IDENTITY_BUCKET`
- `IDENTITY_SMOKE_B2_ENDPOINT`
- `IDENTITY_SMOKE_B2_REGION`
- `IDENTITY_SMOKE_B2_PRIVATE_BUCKET`
- `IDENTITY_SMOKE_B2_PUBLIC_BUCKET`
- `IDENTITY_SMOKE_B2_IDENTITY_BUCKET`

Store access keys as environment secrets:

- `IDENTITY_SMOKE_PRIMARY_ACCESS_KEY_ID`
- `IDENTITY_SMOKE_PRIMARY_SECRET_ACCESS_KEY`
- `IDENTITY_SMOKE_R2_ACCESS_KEY_ID`
- `IDENTITY_SMOKE_R2_SECRET_ACCESS_KEY`
- `IDENTITY_SMOKE_B2_ACCESS_KEY_ID`
- `IDENTITY_SMOKE_B2_SECRET_ACCESS_KEY`

Use dedicated staging-only S3 keys restricted to the six buckets above. Every endpoint must be HTTPS. For each provider, the private, public and identity-evidence bucket names must be distinct and contain `staging` or `smoke`. The primary endpoint must be reachable from the GitHub runner; use a protected self-hosted runner or approved private network path if MinIO is not publicly reachable.

## Run the acceptance workflow

1. Confirm that all nine staging buckets exist and are empty or contain only disposable smoke data.
2. Open **Actions → Identity evidence B2/R2 staging lifecycle → Run workflow**.
3. Select the `identity-evidence-staging` environment and set `confirm_staging_bucket_writes=true`.
4. Review the run summary. A passing run must prove:
   - MinIO primary upload and checksum verification;
   - independent B2 and R2 replication and read-back SHA-256 verification;
   - primary loss and restoration from a verified replica;
   - replica loss and reconciliation;
   - same-size corrupt-object detection and repair;
   - deletion from all three providers and 404 verification;
   - distinct private, public and identity-evidence bucket mapping.

The test deliberately writes, deletes and replaces a same-size corrupt object in the identity-evidence staging bucket. It cleans up its unique smoke object on completion. Do not run it against production buckets.

A green unit or compile check is not a substitute for this staging run. Keep public identity registration disabled until this workflow passes and the reviewer-access and Flutter staging journeys are accepted.
