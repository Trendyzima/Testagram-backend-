# Testagram native platform architecture

The retained Testagram runtime is Appwrite-authenticated and PostgreSQL-owned.
The runtime is Testagram-owned and uses only the services described in this architecture.

## Identity

- Appwrite issues client sessions.
- Backend and Data Centre services validate Appwrite JWTs against the Appwrite
  Account endpoint.
- The protected PostgreSQL identity bridge maps Appwrite IDs to canonical
  Testagram UUIDs. Unknown mappings fail closed.
- Staff roles are stored in PostgreSQL and cannot be self-assigned through
  Appwrite user metadata.

## Product data

- PostgreSQL owns accounts, posts, interactions, privacy, media metadata,
  quota, rate limits and transactional outboxes.
- Object bytes use the native signed storage lifecycle.
- Post media references are object keys stored in `testagram_local.posts.media_ids`.
- Appwrite is not a second product-data write authority.

## Service boundaries

- Flutter calls only the public Backend API for product data.
- The Backend calls Algorithm, Data Centre and Federated Content over private,
  bounded service contracts.
- Algorithm cannot broaden the Backend's authorized candidate set.
- Federation cannot override local account or content authorization.
- Data Centre video and notification endpoints validate Appwrite sessions and
  resolve the same canonical identity bridge.

## Runtime-owned infrastructure

Rate limiting uses PostgreSQL fixed-window counters. Durable asynchronous work
uses Testagram-owned transactional outboxes and idempotent PostgreSQL workers.

## Release gates

Do not claim production readiness from compilation alone. Require all five
repository CI workflows, Appwrite session tests, PostgreSQL integration and
privacy tests, media lifecycle tests, federation signature/deduplication tests,
restore validation and native Flutter route acceptance.
