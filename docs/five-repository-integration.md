> **Superseded:** This is a historical five-repository design note. The authoritative current scope and service-to-service contract is [`docs/six-repository-runtime-integration-contract.md`](six-repository-runtime-integration-contract.md), which covers all six retained repositories. Do not use this historical document to infer current deployment wiring.

# Five-repository integration contract

## Repositories and ownership

| Repository | Owns | Must not own |
|---|---|---|
| [Flutter client](https://github.com/Trendyzima/testagram-flutter-client-) | Native UI, navigation, local interaction state, Appwrite session acquisition | PostgreSQL, object storage credentials, ranking or federation internals |
| [Testagram Backend](https://github.com/Trendyzima/Testagram-backend-) | Appwrite JWT validation, authorization, canonical PostgreSQL, public API, media lifecycle | Supabase/PostgREST runtime, Redis/Upstash cache or generic queue |
| [Data Centre](https://github.com/Trendyzima/Testagram-data-centre-) | Storage nodes, media processing, notifications, platform infrastructure | Client-facing product authorization or separate identity authority |
| [The Algorithm](https://github.com/Trendyzima/the-algorithm) | Feed ranking over authorized candidates | User authorization, database writes or cursor ownership |
| [Federated Content](https://github.com/Trendyzima/federated-content) | Signed inbox/outbox, remote policy and delivery | Local product account authority or Flutter UI |

## Authentication and data flow

1. Flutter signs in with Appwrite and obtains a session JWT.
2. Flutter sends that JWT as a Bearer token to the Backend.
3. The Backend validates the JWT against Appwrite's Account endpoint.
4. The Backend resolves the Appwrite user ID to the canonical Testagram UUID
   through `testagram_identity.appwrite_accounts`.
5. Every product query is authorized in the Backend against PostgreSQL policy
   and privacy state.
6. Specialized calls to Algorithm, Data Centre and Federated Content use
   private service-to-service credentials and bounded request contracts.

## Cross-service contracts

- Backend → Algorithm: authorized candidate IDs, stable cursor context and
  bounded ranking request; returned IDs must be a subset of the candidate set.
- Backend → Data Centre: signed storage lifecycle and private platform API;
  no client-facing credentials or direct Flutter access.
- Backend ↔ Federated Content: signed inbox/outbox acceptance, consent checks,
  idempotent delivery and durable retry state.
- Data Centre video/notification services validate Appwrite sessions and
  resolve canonical account IDs using the shared PostgreSQL identity bridge.

## Dependency boundary

The retained architecture contains no Supabase Auth, PostgREST, Storage,
Realtime or Edge Function runtime, and no Redis/Valkey/Upstash dependency.
Rate limiting uses PostgreSQL counters. Durable asynchronous work uses
service-owned PostgreSQL outboxes. Public media may use HTTP/CDN caching, but
no application cache wrapper is an authority for user or product state.

## Acceptance

A feature is complete only when its native screen, authenticated API method,
Backend authorization, PostgreSQL behavior, service contracts, tests and
migration ledger are all complete. Navigation links and placeholders do not
count as migrated functionality.
