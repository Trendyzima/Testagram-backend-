# Neon PostgreSQL social data plane (staged migration)

## Decision

Appwrite remains the identity provider. The Go Backend validates Appwrite sessions and remains the only public application API and authorization authority. Neon PostgreSQL becomes the canonical store for product data: profiles, posts, social graph, transactional events, conversations, messages and notifications. Federated Content owns ActivityPub protocol state in a separately permissioned PostgreSQL database. Data Centre owns infrastructure and media durability, not product identity or social policy.

XClone must never connect directly to Neon, Appwrite Databases, Federation PostgreSQL or Durable Objects with privileged credentials.

## Capacity and traffic assumptions

The planning workload is 1.5 million database writes/month (about 0.58 writes/second averaged over a 30-day month) and 15 million API requests/month (about 5.8 requests/second averaged over a 30-day month). These averages are not peak capacity estimates. Production acceptance requires burst tests, connection-pool tests, query plans, storage-growth measurement and a realistic realtime concurrency profile.

Use Neon pooled connection strings for horizontally scaled services. Start with a small per-instance pool (Backend default 8; Federation separately bounded) and enforce a global connection budget. Use TLS (sslmode=require or stronger), statement timeouts, request deadlines, bounded result sizes and keyset pagination. Never put a Neon connection string in frontend variables.

## Ownership and service boundaries

- Appwrite: signup, login, password recovery, sessions and account identity only.
- Backend + Neon testagram schema: canonical local profile/social/post/message/notification data, visibility checks and transactional outbox.
- Federation: ActivityPub actors, remote actors, signed inbox/outbox, delivery jobs and protocol-specific replay state. It calls Backend for local identity/eligibility and never creates local users.
- Algorithm: private candidate ranking only. It receives bounded candidate IDs/signals from Backend and returns ordered IDs/scores; Backend rechecks visibility and hydrates canonical rows.
- Data Centre: deployment, private networking, storage nodes, B2/R2 media lifecycle, backups and recovery.
- Cloudflare Durable Objects: live WebSocket coordination, presence and fan-out. It is not the canonical store for posts or messages. Persist each accepted message through Backend/Neon before acknowledging it.
- Upstash: cache/rate-limit/short-lived coordination and QStash scheduling only. Never make Redis the sole copy of a message, post, activity or delivery job.

## Migration safety

This branch adds a PostgreSQL connection package and an additive SQL schema only. It deliberately does not switch live handlers, copy Appwrite documents, alter Appwrite collections, change production routing, or enable retention/deletion. Do not cut over until:
1. a staging Neon database is provisioned with least-privilege roles and TLS;
2. schema migrations are applied and verified;
3. Appwrite profile/post/social data is exported with a stable checkpoint and imported idempotently;
4. row counts, IDs, relationships, timestamps, visibility and media references reconcile;
5. dual-read comparisons pass without serving stale or unauthorized data;
6. write cutover uses a controlled maintenance/dual-write protocol with replayable change capture;
7. rollback is rehearsed and no source data is deleted;
8. API, federation, messaging and realtime load/security tests pass.

The schema uses Appwrite user IDs as immutable text keys. Do not use email as a primary key and do not silently create a new identity during migration.

## Connection settings

NEON_DATABASE_URL must be a TLS PostgreSQL URL. Use Neon’s pooled endpoint for app traffic where appropriate. The connection package bounds each process to at most 32 connections and defaults to 8 open / 2 idle. Set the total across all replicas below the Neon compute connection budget. A local PostgreSQL URL is accepted only for development/test.

## Not production-enabled

This is an implementation foundation, not a completed cutover. Existing fresh API handlers still use the Appwrite Database client for application records until each domain is ported behind a repository interface and the verified migration has been completed. Keep Appwrite authentication operational. Do not point production traffic at this schema yet.
