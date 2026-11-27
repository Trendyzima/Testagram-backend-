# Identity evidence staging acceptance

Identity-verification photographs and documents are stored exclusively in the dedicated private Backblaze B2 identity-evidence bucket managed by the Data Centre media service.

The Backend has no local InstaBox, Cloudflare R2 or general-media fallback for identity evidence. The Backend authorizes the evidence workflow and calls the private Data Centre identity-evidence API. The Data Centre validates and signs uploads, verifies exact byte length/content type/SHA-256, records lifecycle state, serves short-lived authorized downloads and executes deletion.

## Storage boundary

- Identity evidence object bytes: dedicated private Backblaze B2 identity-evidence bucket only.
- Identity evidence metadata sidecars: same dedicated private Backblaze B2 identity-evidence bucket.
- Ordinary media: separate general-media path; InstaBox VPS, Cloudflare R2 and Backblaze B2 may all hold ordinary media according to the general-media replication policy.
- Identity evidence is never written to the InstaBox media bucket, Cloudflare R2, CDN, public bucket or general-media namespace.
- A B2 outage or failed checksum keeps evidence unavailable and identity registration/review fail-closed. There is no provider fallback.

## Staging acceptance

Use the Data Centre repository's `.github/workflows/media-provider-staging-acceptance.yml` workflow with an explicitly confirmed, isolated non-production `media-staging` environment.

The identity-evidence portion must prove:

1. Dedicated identity service token is distinct from the ordinary media service token.
2. Readiness authenticates against the dedicated B2 identity bucket and reports `provider=backblaze_b2`.
3. The ordinary media token cannot call identity-evidence routes, and the identity token cannot call ordinary media routes.
4. A disposable evidence object can be uploaded directly to the dedicated B2 identity bucket.
5. Verification reads the object back from B2 and checks exact byte count, content type and SHA-256 before transitioning to `protected`.
6. Status and authorized GET/HEAD downloads resolve only to Backblaze B2.
7. Wrong-owner access is denied; public/CDN access is unavailable.
8. Idempotent deletion removes the object and its metadata sidecar from the dedicated B2 bucket.
9. A B2 outage, missing object or checksum mismatch fails closed; no R2 or InstaBox identity copy is created or used.

The general-media portion of the same workflow separately verifies ordinary B2/R2 lifecycle and replica restoration. Its R2 credentials and buckets are not used for identity evidence.

## Release gate

A passing unit/CI run is not live provider acceptance. Keep identity registration and evidence review fail-closed until the confirmed staging provider workflow, Backend-to-Data-Centre authenticated flow, reviewer authorization, deletion and Flutter staging journey pass. Production bucket changes, deployment and registration enablement remain separately controlled manual operations.
