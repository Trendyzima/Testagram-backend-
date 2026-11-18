# Identity evidence provider staging acceptance

This acceptance is separate from ordinary Backend CI and must never use production buckets or production credentials. The native identity-evidence namespace is stored in dedicated B2 and R2 buckets by the Data Centre media service; ordinary media buckets are not a substitute.

## Required staging-only credentials

Configure these repository or protected-environment secrets in the Data Centre repository:

- `MEDIA_SMOKE_B2_ENDPOINT`
- `MEDIA_SMOKE_B2_REGION`
- `MEDIA_SMOKE_B2_ACCESS_KEY_ID`
- `MEDIA_SMOKE_B2_SECRET_ACCESS_KEY`
- `MEDIA_SMOKE_R2_ACCESS_KEY_ID`
- `MEDIA_SMOKE_R2_SECRET_ACCESS_KEY`

For R2, also configure `MEDIA_SMOKE_R2_ENDPOINT`, or configure `CLOUDFLARE_ACCOUNT_ID` so the workflow can derive the R2 S3 endpoint. `MEDIA_SMOKE_R2_REGION` defaults to `auto`.

These keys must be staging-only and scoped to the staging media and identity-evidence buckets. Do not substitute `B2_S3_*`, `CLOUDFLARE_R2_*`, or production backup credentials.

The workflow defaults to these separate bucket names:

- B2 media: `testagram-media-staging-b2`
- B2 identity evidence: `testagram-identity-evidence-staging-b2`
- R2 media: `testagram-media-staging-r2`
- R2 identity evidence: `testagram-identity-evidence-staging-r2`

You may override them with `MEDIA_SMOKE_B2_BUCKET`, `MEDIA_SMOKE_B2_IDENTITY_BUCKET`, `MEDIA_SMOKE_R2_BUCKET`, and `MEDIA_SMOKE_R2_IDENTITY_BUCKET`. Every bucket name must contain `staging` or `smoke`; identity-evidence buckets must be distinct from the corresponding media buckets. The workflow creates missing buckets and explicitly sets anonymous access to `none`.

## Run the acceptance workflow

1. Confirm the four staging buckets contain only disposable smoke data.
2. In the Data Centre repository, open **Actions → Live media provider smoke → Run workflow**.
3. Set `confirm_live_provider_writes=true`. Without this explicit confirmation, the job does not run.
4. Review the run summary and logs. A passing identity-evidence test must prove:
   - identity-specific readiness checks can write, read and delete a temporary object in both dedicated buckets;
   - the identity upload route issues an HTTPS upload URL and accepts the object in B2;
   - content type, byte length and SHA-256 are verified;
   - the durable outbox replicates the evidence to R2 and the object reaches `protected`;
   - cross-application evidence download is denied;
   - authorized download returns the exact bytes;
   - loss of the B2 primary is recovered from the verified R2 replica;
   - deletion reaches both providers and the object is no longer readable.

The same workflow also runs the ordinary B2/R2 media round-trip and replication/restore/delete checks against separate staging media buckets. It uses an isolated PostgreSQL database for the smoke lifecycle.

A green unit or compile check is not a substitute for this live staging run. Keep public identity registration disabled until this provider smoke, the Backend-to-Data-Centre readiness checks, reviewer access, independent approval, and Flutter staging journeys are accepted.
