# Backend media provider staging acceptance

This workflow validates the Backend-owned virtual-storage path. It is separate from the Data Centre's B2/R2 media-service lifecycle and from the CDN's deployed byte-delivery smoke test.

## GitHub Actions environments

Create two protected, non-production environments. Do not copy production provider credentials into either environment.

### `identity-evidence-staging`

Required environment variables:
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

Required environment secrets:
- `IDENTITY_SMOKE_R2_ACCESS_KEY_ID`
- `IDENTITY_SMOKE_R2_SECRET_ACCESS_KEY`
- `IDENTITY_SMOKE_B2_ACCESS_KEY_ID`
- `IDENTITY_SMOKE_B2_SECRET_ACCESS_KEY`

The job creates three disposable local MinIO buckets itself. The six cloud buckets must be dedicated staging/smoke buckets, distinct by provider and namespace, with no production bucket names. Keys must be scoped only to those buckets.

Run **Actions → Backblaze B2 storage smoke test** with `confirm_live_provider_writes=true`. This executes the Backend virtual-storage identity-evidence replication lifecycle, including cross-provider integrity, restore, repair and deletion tests.

### `media-staging`

Required environment variables:
- `MEDIA_STAGING_B2_ENDPOINT`, `MEDIA_STAGING_B2_REGION`, `MEDIA_STAGING_B2_BUCKET`
- `MEDIA_STAGING_B2_PRIVATE_BUCKET`, `MEDIA_STAGING_B2_PUBLIC_BUCKET`, `MEDIA_STAGING_B2_IDENTITY_BUCKET`
- `MEDIA_STAGING_R2_ENDPOINT`, `MEDIA_STAGING_R2_REGION`
- `MEDIA_STAGING_R2_PRIVATE_BUCKET`, `MEDIA_STAGING_R2_PUBLIC_BUCKET`, `MEDIA_STAGING_R2_IDENTITY_BUCKET`

Required environment secrets:
- `MEDIA_STAGING_B2_ACCESS_KEY_ID`, `MEDIA_STAGING_B2_SECRET_ACCESS_KEY`
- `MEDIA_STAGING_R2_ACCESS_KEY_ID`, `MEDIA_STAGING_R2_SECRET_ACCESS_KEY`

All bucket names must explicitly contain `ci`, `staging` or `smoke`, must not contain `prod` or `production`, and must be distinct across private, public and identity namespaces. The single-provider B2 smoke uses its own `MEDIA_STAGING_B2_BUCKET`; the three-provider job uses the six namespace buckets and an isolated ephemeral MinIO primary.

Run **Actions → Backblaze B2 storage smoke test** with `confirm_b2_object_write=true` for the B2-only round trip. Run it with `confirm_live_provider_writes=true` only for the identity-evidence job. The three-provider media job must have its own explicit confirmation input before it is enabled; it must never inherit the identity-evidence confirmation implicitly.

## What this does not prove

These tests do not prove that the deployed Backend, Data Centre media service, video worker and Go CDN are using the same immutable release set. They do not prove CDN public/private cache behavior. Those require the separate authenticated staging API and Android video acceptance workflows, exact deployed image digests, and a same-deployment acceptance record.
