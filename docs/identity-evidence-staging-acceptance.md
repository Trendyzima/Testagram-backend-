# Identity evidence provider staging acceptance

This procedure exercises the real three-provider identity-evidence lifecycle. It is intentionally separate from ordinary CI and must never use production buckets or production credentials.

## Required GitHub environment

Create the GitHub Actions environment named `identity-evidence-staging`. The workflow creates three disposable local MinIO buckets itself; do not configure external primary MinIO credentials. Store the following as environment variables (not secrets):

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

Store dedicated staging-only access keys as environment secrets:

- `IDENTITY_SMOKE_R2_ACCESS_KEY_ID`
- `IDENTITY_SMOKE_R2_SECRET_ACCESS_KEY`
- `IDENTITY_SMOKE_B2_ACCESS_KEY_ID`
- `IDENTITY_SMOKE_B2_SECRET_ACCESS_KEY`

Use dedicated S3 keys restricted to the six cloud buckets above. Every cloud endpoint must be HTTPS. For each provider, the private, public and identity-evidence bucket names must be distinct and contain `ci`, `staging` or `smoke`; production-named buckets are rejected. The three local MinIO buckets are ephemeral and created by the job.

## Run the acceptance workflow

1. Confirm that the six dedicated cloud buckets exist and contain only disposable smoke data; the workflow creates the three isolated MinIO buckets at runtime.
2. Create and configure both `identity-evidence-staging` and `media-staging` environments as described in [media provider staging acceptance](media-provider-staging-acceptance.md). The current workflow shares `confirm_live_provider_writes` between its identity-evidence and three-provider media jobs, so both environments are required for that run.
3. Open **Actions → Backblaze B2 storage smoke test → Run workflow** and set `confirm_live_provider_writes=true`. Each job attaches its own environment and reads only environment-scoped staging variables and secrets.
4. Review the run summary. A passing run must prove:
   - MinIO primary upload and checksum verification;
   - independent B2 and R2 replication and read-back SHA-256 verification;
   - primary loss and restoration from a verified replica;
   - replica loss and reconciliation;
   - same-size corrupt-object detection and repair;
   - deletion from all three providers and 404 verification;
   - distinct private, public and identity-evidence bucket mapping.

The test deliberately writes, deletes and replaces a same-size corrupt object in the identity-evidence staging bucket. It cleans up its unique smoke object on completion. The identity-evidence job uses only the documented environment-scoped `IDENTITY_SMOKE_*` variables and secrets; it does not fall back to repository-wide or production B2/R2 credentials. Do not run it against production buckets.

A green unit or compile check is not a substitute for this staging run. Keep public identity registration disabled until this workflow passes and the reviewer-access and Flutter staging journeys are accepted.
