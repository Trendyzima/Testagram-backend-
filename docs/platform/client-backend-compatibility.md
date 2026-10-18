# Testagram client/backend compatibility contract

This document is the shared release contract for the Testagram web client and the self-hosted Go API. It describes the current media path; it is not a claim that a production deployment has passed end-to-end testing.

## One identity authority

The browser client must obtain its access token from the same Supabase-compatible Auth instance that the Go API trusts. Configure the client `VITE_SUPABASE_URL` and the API's server-side `SUPABASE_URL`/JWT verification settings for the same self-hosted instance. The primary project must issue the user IDs used as media owners. Do not mix access tokens from the legacy hosted projects with a self-hosted API: JWT issuer, signing keys, and user UUIDs may differ.

Never expose service-role keys, MinIO root credentials, S3 signing secrets, or internal platform/control-plane tokens in `VITE_*` variables or browser bundles.

## Media API contract

All authenticated JSON endpoints use a bearer access token and the response envelope `{ ok, data, error, request_id }`. The client must treat any non-2xx status, `ok: false`, or non-null `error` as failure.

| Step | Request | Required behavior |
| --- | --- | --- |
| Create intent | `POST /api/media/upload-intent` with `name`, `content_type`, `visibility`, `size_bytes` | Returns `data.key`, `data.url`, expiry metadata and, for public media, a configured public URL |
| Transfer bytes | `PUT data.url` with exact `Content-Type` and the file body | For virtual storage the URL is relative to the API origin and points to `/api/media/upload?key=...`; include the same user's bearer token |
| Complete | `POST /api/media/complete` with `key`, `visibility`, `content_type`, `checksum_sha256` | Server verifies object metadata and actual SHA-256 bytes before publishing an asset |
| Attach | `POST /api/media/attach` with `key`, `post_id` | Links a ready asset only after server-side ownership and post authorization checks |
| Private retrieval | `POST /api/media/private-download-url` with `key` | Returns a short-lived URL only to an authorized owner |
| Delete | `POST /api/media/delete` with `key` | Uses owner-scoped deletion and durable cleanup state |

Public client uploads currently support JPEG, PNG, WebP, AVIF, GIF, MP4, and WebM, with a maximum of 20 MiB. The server remains authoritative: client validation is for UX, not security. Client and server limits, proxy limits, MIME allowlists, and storage policy must be changed together.

The SHA-256 digest must be calculated from the exact file bytes transferred. Do not substitute an S3 ETag. A failed PUT must never be followed by completion. Failed or interrupted uploads should be treated as failed and reconciled by the server's cleanup worker; clients should request a new intent rather than replaying an intent already claimed.

## Local/self-hosted client mode

Set `VITE_TESTAGRAM_SELF_HOSTED=true`, `VITE_SUPABASE_URL`, `VITE_SUPABASE_PUBLISHABLE_KEY`, and `VITE_TESTAGRAM_API_URL` to endpoints owned by the operator. Self-hosted mode must fail closed if required settings are absent; it must not silently route authentication or media to hosted Testagram projects. Unless a second self-hosted project is explicitly configured, the secondary client should use the same local Supabase-compatible endpoint.

Use an explicit local/test origin allowlist in the API's `ALLOWED_ORIGINS`. Configure the browser origin, API origin, and media public origin consistently, including HTTPS in non-local deployments.

## Release acceptance tests

Run against an isolated stack with test accounts and disposable buckets/database:

1. Build and type-check the client; run backend `go test -race ./...`, `go vet ./...`, and the production build.
2. Start a fresh self-hosted stack and separately test applying migrations to a copy of an existing database. PostgreSQL init scripts only run for a fresh data directory.
3. Sign in through the configured self-hosted Auth instance; confirm the API accepts that token and rejects missing, expired, wrong-issuer, and wrong-user tokens.
4. Upload an exact-size image, compare SHA-256 against stored bytes, complete it, attach it to an authorized post, and render the returned public URL.
5. Reject wrong checksum, wrong MIME, oversized fixed-length and chunked bodies, expired intents, cross-account intent use, and concurrent/replayed PUTs. Confirm failed objects are queued for cleanup.
6. Confirm private downloads are owner-scoped and public media is not accidentally served from a private bucket.
7. Delete an asset, wait for the cleanup worker, and verify both metadata and object state.
8. Restore a verified backup into an isolated database and validate schema plus representative rows before declaring recovery ready.

Do not enable `VIRTUAL_STORAGE_ENABLED` for production until these tests pass against the actual target configuration. A successful compile or archive listing is not a substitute for end-to-end testing or a restore drill.
