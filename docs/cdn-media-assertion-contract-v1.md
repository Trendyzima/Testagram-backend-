# Testagram CDN media assertion contract v1

Backend is the sole authority for media ownership, visibility, moderation state and viewer authorization. A storage presigned URL is not a CDN authorization assertion.

## Private media capability
- Backend issues only after authenticating the viewer and reading a canonical `testagram_media.assets` row.
- The row must match the exact object key, `owner_id = viewer`, `visibility = private`, and `status = ready`.
- Assertion format: `v1.<base64url-json>.<base64url-hmac-sha256>`.
- Claims: `v`, `iss=testagram-backend`, `aud=testagram-cdn`, `sub` (viewer ID), `key` (canonical object key), `visibility=private`, `iat`, `exp`, and random `jti`.
- HMAC-SHA256 uses a dedicated `CDN_MEDIA_ASSERTION_SECRET` of at least 32 random bytes. It is not an Appwrite key, storage credential, federation token, or CDN origin service token.
- Maximum lifetime is five minutes; default lifetime is two minutes. Validators reject future-issued, expired, wrong-audience, wrong-viewer, wrong-key, non-private, malformed and overlong claims.
- Client sends `Authorization: Bearer <assertion>` on the media request. Never put assertions in query strings, analytics, referrers or logs.
- An assertion is bound to one object and one viewer; it cannot be replayed for a different key or viewer.

## Public media
Public objects do not receive private assertions. They are eligible only when Backend metadata says `visibility=public` and `status=ready`. Public delivery must use a distinct public namespace or an authoritative origin response with explicit `X-Testagram-Visibility: public`. Never infer public visibility from a missing token or missing secret.

## Edge requirements
Go CDN and Cloudflare Worker must implement the exact v1 encoding, HMAC input, claim validation, key canonicalization and expiry rules. Private responses must be `Cache-Control: private, no-store`, bypass shared/stale caches, and fail closed. Public/private cache keys must not collide. Every HLS manifest, segment, initialization fragment and encryption-key request must carry viewer authorization; private HLS responses must not be cached.

## Rollout
The legacy `/api/media/private-download-url` remains unchanged while clients migrate. The new Backend route is additive. Do not enable CDN assertion delivery or remove legacy storage URL support until Go CDN, Worker, Flutter clients and physical end-to-end tests pass.
