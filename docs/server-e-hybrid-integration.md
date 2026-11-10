# Testagram Native Integration Contract

This contract describes the retained five-repository runtime. It supersedes
older hybrid instructions that routed product traffic through Supabase,
PostgREST, Redis, Valkey or Upstash.

## Ownership

- Flutter is the only product UI and sends Appwrite JWTs to the Backend.
- The Backend validates Appwrite sessions, owns authorization and canonical
  PostgreSQL state, and exposes the public product API.
- The Data Centre owns storage nodes, video processing, notifications and
  platform infrastructure.
- The Algorithm ranks only Backend-authorized feed candidates.
- Federated Content validates signed remote activities and owns federation
  delivery state.

## Prohibited runtime paths

- No Supabase Auth, PostgREST, Storage, Realtime or Edge Function runtime.
- No Redis/Valkey/Upstash clients, credentials, URLs, cache wrappers or generic
  Redis queue APIs.
- No Flutter-to-database, Flutter-to-ranker or Flutter-to-federation calls.
- No service-role credential in browser or mobile configuration.

## Data and async contracts

- PostgreSQL is canonical for accounts, posts, interactions, media metadata,
  authorization counters and transactional outboxes.
- Appwrite owns session issuance. The Backend validates the JWT with Appwrite
  and resolves the canonical Testagram UUID from the identity bridge.
- Media object bytes are accessed only through the Backend's signed storage
  lifecycle. Post media references are native object keys.
- Feed ranking is a bounded internal call. The Backend rejects ranker IDs not
  present in the authorized candidate set.
- Federation delivery and media cleanup use durable PostgreSQL outboxes/jobs,
  idempotency keys, bounded retries and reconciliation.

## Release gates

- All five repository CI workflows pass on the coordinated commits.
- Flutter dependency-boundary checks pass.
- Backend PostgreSQL integration tests cover privacy, media references and
  interaction projections.
- Appwrite session verification and identity mapping fail closed.
- Restore, backup, signed media and federation contract tests pass before
  production cutover.
