# Testagram API v1 consumer guide

## Ownership and routing

- XClone (web/Android/desktop) calls only the public Testagram Backend `/v1` API using a short-lived Appwrite JWT.
- Backend verifies identity and owns product authorization, canonical profile/post data, moderation decisions, visibility, media authorization and response shaping.
- Backend calls Federation through `/internal/v1/federation/*`, Algorithm through the private `/v1/feed/rank` endpoint, and Data Centre through the private platform/infrastructure APIs.
- Federation, Algorithm and Data Centre do not accept browser credentials and never become alternative public product APIs.

## Transport requirements

- Send `Authorization: Bearer <short-lived Appwrite JWT>` for public authenticated endpoints.
- Send `X-Testagram-API-Version: 1` and a unique `X-Request-ID` on every request.
- Send `Accept: application/json`; use JSON bodies and `Content-Type: application/json` for writes.
- Send `Idempotency-Key` for every endpoint whose OpenAPI operation marks it required. Persist the same key across an application-level retry of the same user intent.
- Never send cookies, Appwrite server keys, Neon credentials, B2/R2 credentials, internal service tokens or Algorithm/Federation/Data Centre secrets from a client.
- Treat responses as private/no-store unless the operation explicitly declares public caching. Use the response request ID for support/debug correlation.

## Retry and pagination

- Retry only safe GET requests, and only for transient 429/502/503/504 responses with bounded exponential backoff and Retry-After support.
- Do not automatically retry POST/PUT/DELETE. Mutations require idempotency semantics before retry.
- Feed pagination uses the opaque `before` cursor returned by Backend. Do not synthesize cursors or offset-paginate the feed.
- Federation outbox pagination uses the opaque `after` cursor. Persist `next_cursor` after every non-empty page, including the final page where `has_more=false`.

## Implemented public API surface

The OpenAPI document is the authoritative route list. Current public capabilities include authenticated profile/legal/Federation preference reads and writes, home-feed reads, moderated text-post creation, content moderation checks, and authorized media upload/download/restore operations. Any feature absent from the OpenAPI document is not a supported production route and must fail closed instead of falling back to Supabase or a direct provider.

## Private service calls

- Backend -> Federation: actor discovery and durable public-post outbox.
- Backend -> Algorithm: bounded, already-authorized candidate IDs and ranking features; Backend rechecks visibility after ranking.
- Backend -> Data Centre: private infrastructure/cache/queue/media operations. Data Centre never writes canonical social records.
