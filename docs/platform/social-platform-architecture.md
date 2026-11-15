# Testagram social platform architecture

Testagram adopts proven product and protocol patterns from X, Bluesky and Mastodon while retaining its own identity, data ownership, moderation and release controls. This is an implementation map, not a claim that Testagram is already an X, AT Protocol or Mastodon client.

## X-inspired product and ranking

- The main feed supports server-defined `for_you`, `following`, `latest`, `media` and `discover` lanes.
- Backend authenticates the viewer, selects the bounded candidate set, applies blocks/mutes/privacy/visibility, and performs a final visibility recheck after ranking.
- The Testagram Feed service in `the-algorithm/testagram-feed` applies bounded relevance signals, recency, quality, constructive conversation and author diversity.
- Likes, replies, reposts, bookmarks and quote/thread surfaces remain Backend-owned canonical records.
- Ranker failure falls back to chronological ordering; it cannot make a post publish or an account authenticate.

## Bluesky-inspired composable feeds

The named feed lanes are Testagram-owned feed generators: the client chooses a lane identifier, while Backend owns candidate discovery and permissions and the Algorithm service returns ordering only. This creates a safe extension point for future community-curated feeds without letting a feed generator bypass blocks, private-account rules or moderation.

Current lanes:
- `for_you`: personalized ranked home feed.
- `following`: accepted-follow graph with ranking.
- `latest`: chronological, no ranker dependency.
- `media`: authorized posts containing media, ranked.
- `discover`: public posts from accounts the viewer does not follow, ranked.

The current implementation does **not** implement AT Protocol, Bluesky PDS hosting, Bluesky login, DID resolution or cross-posting. Those require a separate, opt-in protocol adapter, explicit account linking, lexicon mapping, consent and moderation semantics. Do not treat a Bluesky handle as a Testagram identity.

## Mastodon-inspired federation

The separate Federated Content service implements the ActivityPub protocol boundary: local actors and signing keys, remote actor/key cache, outbound Create/Note activity, remote follower records, durable outbox and signed delivery workers. Backend remains authoritative for local identity, account consent, post visibility, blocks and whether an object may be published.

The following remain release gates before broad Mastodon interoperability or public inbound federation:
- HTTP Signature and Digest/Date verification, replay protection and durable inbox acceptance.
- Follow/Undo acceptance and authoritative local follower projection.
- Inbound Create/Update/Delete/Like/Announce projection through Backend authorization and moderation.
- Remote interaction publishing and follower fan-out with recipient deduplication.
- Staging interoperability tests against Mastodon/GoToSocial peers, retry/dead-letter operations and SSRF controls.

Do not enable inbound public routing or federation delivery merely because a route or worker exists. Respect per-account federation consent and existing protected-account settings.

## Cross-platform trust boundaries

- Backend owns local identity, session verification, profiles, follows, blocks, mutes, privacy, moderation, canonical posts and media ownership.
- Algorithm receives only bounded Backend-authorized candidate IDs and server-derived, non-sensitive features. It cannot query the product database or authorize a record.
- Federated Content owns protocol actor keys, remote caches, inbox/outbox and delivery state. It cannot create local accounts or grant local access.
- CDN delivers bytes only after Backend-issued authorization. It is not a feed or identity authority.
- Testagram API owns versioned public and private contracts, not runtime product data.

## Release sequencing

1. Ship and evaluate the named feed lanes with chronological fallback and visibility regression tests.
2. Complete the authenticated-activity watchdog and node activation path without suspending Backend, auth, databases or public ingress.
3. Finish ActivityPub inbound security, Follow/Undo and projection tests before enabling Mastodon inbound federation.
4. Design an isolated AT Protocol adapter only after product requirements for Bluesky account linking, cross-posting and identity consent are approved.
