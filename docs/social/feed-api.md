# PostgreSQL-backed social feed API

`GET /api/social/feed` is the canonical Go API read path for local Testagram posts.

## Request

- Authentication: `Authorization: Bearer <session-token>`; the Go API resolves the token using the explicitly configured authentication provider and then requires a linked canonical Testagram account.
- `mode`: `for_you` (default) or `latest`.
- `limit`: 1–50; defaults to 30.
- `cursor`: opaque URL-safe cursor returned by the previous response. Cursors are mode-specific; do not construct or edit them.

## Visibility and abuse controls

The SQL query filters deleted posts, inactive accounts, private/follower-only visibility, protected accounts, blocks in either direction, and accounts muted by the viewer. The endpoint is rate-limited per canonical user. It returns only the post and author fields needed by the feed; it does not expose storage credentials or internal moderation metadata.

## Ranking and pagination caveat

`for_you` currently uses a transparent heuristic combining likes, replies, reposts and post age. Pagination uses keyset cursors, but engagement counts can change between page requests and therefore move a post across the ranking boundary. This first iteration is suitable for controlled integration testing; do not claim snapshot-consistent ranking until feed-session snapshots or an equivalent immutable ranking snapshot are implemented and load-tested. `latest` is the stable chronological mode.

## Rollout boundary

The migration adds feed-oriented indexes and is mounted after the social-core migration for fresh self-hosted PostgreSQL volumes. Existing database volumes require a separately reviewed migration plan after backups and restore tests. The frontend client exposes `socialApi.listFeed`, but the existing mixed local/thread/federated homepage aggregator is not switched over yet; media-ID hydration and the UI response adapter must be completed first.
