# Testagram client/backend compatibility contract

This document is the shared release contract for the Testagram web client and the self-hosted Go API. It describes the current media path; it is not a claim that a production deployment has passed end-to-end testing.

## One identity authority

The Flutter client authenticates directly with the Testagram Backend through the native authentication routes. The Backend is the sole authority for password verification, canonical user IDs, access-token issuance, PostgreSQL-backed sessions, refresh rotation, account eligibility and revocation.

- Use `TESTAGRAM_API_URL` as the single public Backend API origin. The current Flutter client defaults to `https://api.testagram.site`.
- `NativeAuthClient` obtains and refreshes credentials through the Backend. It does not delegate login to a hosted identity provider or an external account service.
- Access JWTs remain in memory. Only the opaque rotating refresh credential is stored in platform secure storage.
- Every protected Backend request validates the token and rechecks the live session and account state. A valid JWT signature alone is not sufficient.
- Sign-out must complete server-side revocation before the client reports a successful logout. Failed revocation preserves the session so the user can retry.
- Do not add external identity-provider SDKs, provider URL/key configuration, provider JWT fallbacks or alternate identity authorities to the client.

Never expose private signing keys, session secrets, service credentials, object-store credentials or internal platform tokens in Flutter build-time defines or browser bundles.

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

## Client origin and deployment configuration

The Flutter client uses the configured HTTPS Testagram Backend origin for authentication and public product APIs. Staging builds must set `TESTAGRAM_API_URL` to the staging Backend origin and must use dedicated staging identities and disposable media. Production credentials must never be used in staging tests.

- Require HTTPS for non-local deployments.
- Keep allowed browser origins, Backend API origin and public media origin aligned.
- Fail closed when a required origin is missing or invalid; do not silently fall back to an unrelated hosted service.
- Keep private service credentials and object-store secrets server-side. The client receives only Backend-issued, narrowly scoped upload/download instructions.
- Do not add an external identity provider, hosted database or alternate account authority as a Testagram runtime dependency.

## Release acceptance tests

Run against an isolated stack with test accounts and disposable buckets/database:

1. Build and type-check the client; run backend `go test -race ./...`, `go vet ./...`, and the production build.
2. Start a fresh self-hosted stack and separately test applying migrations to a copy of an existing database. PostgreSQL init scripts only run for a fresh data directory.
3. Sign in through the Testagram-native Backend route; confirm the API accepts that session and rejects missing, expired, wrong-issuer, wrong-audience, revoked, and wrong-user tokens.
4. Upload an exact-size image, compare SHA-256 against stored bytes, complete it, attach it to an authorized post, and render the returned public URL.
5. Reject wrong checksum, wrong MIME, oversized fixed-length and chunked bodies, expired intents, cross-account intent use, and concurrent/replayed PUTs. Confirm failed objects are queued for cleanup.
6. Confirm private downloads are owner-scoped and public media is not accidentally served from a private bucket.
7. Delete an asset, wait for the cleanup worker, and verify both metadata and object state.
8. Restore a verified backup into an isolated database and validate schema plus representative rows before declaring recovery ready.

Do not enable `VIRTUAL_STORAGE_ENABLED` for production until these tests pass against the actual target configuration. A successful compile or archive listing is not a substitute for end-to-end testing or a restore drill.
