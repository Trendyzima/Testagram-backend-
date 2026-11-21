# Testagram CDN media assertion contract v1

Testagram Backend is the sole authority for media ownership, visibility, moderation state and viewer authorization. A storage presigned URL is not a CDN authorization assertion. There is no delegated external API issuer in this contract.

## Private media capability

- The native Backend route `POST /api/media/cdn-download-url` authenticates the viewer, resolves the canonical user ID from the native session, checks the asset owner/visibility/readiness and issues the short-lived assertion.
- The trusted origin route `GET|HEAD /internal/cdn/v1/{key...}` requires a separate `X-Testagram-Origin-Auth` service credential and rechecks the canonical `testagram_media.assets` row before generating a short-lived storage URL.
- The row must match the exact object key and canonical `owner_id`; it must be `visibility = private` and `status = ready`. Backend performs the viewer/post visibility decision before issuing the assertion. The origin independently checks the owner and ready state.
- Assertion format: `v1.<base64url-json>.<base64url-hmac-sha256>`.
- Claims: `v`, `iss=testagram-backend`, `aud=testagram-cdn`, `sub` (authorized viewer ID), `owner` (canonical media owner ID), `key` (exact canonical object key), `visibility=private`, `iat`, `exp`, and random `jti`. Older owner-only assertions may omit `owner`; validators interpret the subject as owner only for those tokens.
- HMAC-SHA256 uses a dedicated `CDN_MEDIA_ASSERTION_SECRET` of at least 32 random bytes. It is not a storage credential, federation token or CDN origin service token.
- Maximum lifetime is five minutes; default lifetime is two minutes. Validators reject future-issued, expired, wrong-audience, wrong-viewer, wrong-key, non-private, malformed and overlong claims.
- The client sends `Authorization: Bearer <assertion>` on the media request. Never put assertions in query strings, analytics, referrers or logs.
- The edge verifies that the key's owner segment matches `owner`, while `sub` records the viewer whose visibility decision authorized issuance. The token is still a short-lived bearer capability; do not expose it to another viewer.

## Public media

Public objects do not receive private assertions. They are eligible only when Backend metadata says `visibility=public` and `status=ready`. Public delivery must use a distinct public namespace or an authoritative origin response with explicit `X-Testagram-Visibility: public`. Never infer public visibility from a missing token or missing secret.

## Edge requirements

Go CDN and Cloudflare Worker must implement the exact v1 encoding, HMAC input, claim validation, key canonicalization and expiry rules. Private responses must be `Cache-Control: private, no-store`, bypass shared/stale caches and fail closed. Public/private cache keys must not collide. The v1 assertion is bound to one exact object key. A player cannot reuse one assertion for different HLS manifests, segments, initialization fragments or encryption keys. HLS is not rollout-ready until Backend issues per-object assertions or a separately reviewed playback-session/prefix capability and the player can refresh/attach it for every subrequest. Private HLS responses must not be cached.

## Rollout

The Go CDN sends the bearer assertion to Backend as `X-Testagram-Viewer-Authorization` and authenticates itself with a separate `X-Testagram-Origin-Auth` service credential. Backend verifies the assertion and re-reads the canonical asset row before generating a short-lived storage URL; it streams the object with `private, no-store`. The trusted origin route is `/internal/cdn/v1/{key...}` and is not a public bypass: it requires both headers and the ready private asset check.

The assertion-based route `POST /api/media/cdn-download-url` is additive. The existing `POST /api/media/private-download-url` remains available for client migration and uses Backend-native authentication/authorization. Do not remove it or enable CDN assertion delivery until Go CDN, Worker, Flutter clients and physical end-to-end tests pass.
