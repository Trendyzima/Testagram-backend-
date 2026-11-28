# Authenticated staging acceptance: native identity and media

The manual **Video end-to-end staging acceptance** workflow is the combined user-facing acceptance path. It runs, in order, native authentication lifecycle, private Data Centre media lifecycle through the Backend and CDN, creator-video processing, and the real Flutter Android feed test.

It requires all three explicit workflow confirmations:
- `confirm_staging_only=true`
- `confirm_public_feed_test=true`
- `confirm_video_cleanup=true`

The workflow uses the `staging` GitHub Actions environment. It does not deploy services or change production.

The Android AuthScreen integration step runs the Flutter default branch and executes `integration_test/native_auth_staging_test.dart`. The matching native-auth changes must be merged to both default branches before this combined workflow can validate the deployed release.

## Required staging environment

Environment variables:
- `TESTAGRAM_STAGING_API_URL` — HTTPS origin of the isolated staging Backend API.
- `TESTAGRAM_STAGING_FRESH_MEDIA_ENABLED=true` — confirms the staging Backend is configured with FRESH_MEDIA_ENABLED.
- `TESTAGRAM_STAGING_CDN_BASE_URL` — HTTPS origin of the staging private-media CDN.
- `TESTAGRAM_STAGING_B2_HOST` — hostname only for the staging Backblaze B2 S3 endpoint.
- `TESTAGRAM_STAGING_R2_HOST` — hostname only for the staging Cloudflare R2 S3 endpoint.

Environment secrets:
- `TESTAGRAM_AUTH_SMOKE_EMAIL`
- `TESTAGRAM_AUTH_SMOKE_PASSWORD`

The smoke identity must be a dedicated, active and email-verified staging account. It may be created through ordinary public signup and real mailbox verification; identity evidence review or two-person approval is not required for a basic account. This combined workflow does not itself create the account or prove verification-mail delivery. The native-auth smoke deliberately replays a refresh token and calls `logout-all`, which revokes every session belonging to that identity. Never use a human, shared, or production account for this workflow.

The CDN and object-store hostnames must be staging-only. The API, CDN, B2 and R2 hostnames must also be four distinct origins, so one endpoint cannot masquerade as separate provider replication. The media acceptance script rejects canonical production Testagram hosts, requires clean HTTPS origins, refuses redirects for signed object URLs, and never prints bearer tokens, signed URLs or CDN authorization assertions.

## What the combined workflow proves

1. **Native authentication:** login, authenticated profile access, refresh-token rotation, refresh replay revocation, logout and logout-all.
2. **Authenticated private media:** create a private upload intent, upload through the Backend's bounded gateway, compute/verify SHA-256, and require the Data Centre media service to reach protected/ready state.
3. **B2/R2 delivery:** fetch the private object through the Backend's owner-authorized download grant and verify exact PNG bytes against the configured staging B2/R2 hostnames.
4. **CDN authorization:** anonymous private-object access is denied; the viewer assertion permits exact byte delivery and range requests; private responses retain `private, no-store`.
5. **Durable deletion:** Backend deletion purges CDN state, deletes Data Centre B2/R2 objects and metadata sidecars, and the script confirms private-origin and CDN access are gone.
6. **Creator video:** the existing video acceptance harness uploads, processes, publishes a disposable staging video, verifies HLS playback and engagement, then cleans it up.
7. **Flutter native authentication:** the actual AuthScreen signs in on Android, the refresh credential survives a new NativeAuthClient instance through platform secure storage, refresh rotation succeeds, and logout revokes the access session.
8. **Flutter video:** the exact checked-out Flutter source SHA is recorded and the staging video feed is exercised on the same Android emulator.

The workflow summary records the Backend acceptance-harness SHA and Flutter client SHA. Those are source revisions, not proof of deployed service image digests. Record the Backend, Data Centre, video worker and CDN immutable image digests separately for a same-deployment release certificate.

## Separate provider-level tests

The Backend **Backblaze B2 storage smoke test** and Data Centre **Data Centre media provider staging acceptance** workflows exercise provider-specific replication, repair, restore, metadata-sidecar and deletion paths. See [Backend media provider staging acceptance](media-provider-staging-acceptance.md). These require the dedicated `media-staging` environment and its own environment-scoped credentials.

## Remaining acceptance boundary

This combined workflow tests an already verified staging identity. It does not prove new-account email delivery, token/OTP verification, or signup-to-login completion; those require the separate real-mailbox acceptance described in `native-auth-acceptance.md`. Identity-evidence intake, independent reviewer decisions, two-person provisioning approval and staff WebAuthn recovery remain separate privileged-workflow acceptance. Do not make those workflows prerequisites for ordinary public signup.
