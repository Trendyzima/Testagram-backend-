# Backend Neon and realtime staged integration contract

## Current branch scope

This branch adds:
- a TLS-enforcing PostgreSQL connection package with a bounded pool;
- an additive social-data schema for profiles, posts, relationships, outbox, conversations, messages and notifications;
- short-lived, HMAC-signed, conversation-scoped realtime tickets;
- tests for DSN validation and ticket expiry/scope/tampering.

It does not yet route the existing fresh API handlers to Neon. The current fresh API still reads/writes Appwrite Database documents for product data, and the existing media lifecycle still depends on its documented Appwrite collections. Do not remove Appwrite Database collections or credentials until each domain is ported and the data reconciliation/cutover has been approved.

## Required next implementation

1. Introduce repository interfaces for profiles, posts, social graph, notifications and messaging.
2. Port the handlers domain-by-domain from Appwrite DocumentsDB to Neon, preserving Appwrite JWT validation as identity-only.
3. Implement conversation membership, block/privacy checks, idempotent message creation and keyset pagination in Backend.
4. Issue realtime tickets only after Appwrite authentication and a Neon membership check. Use the ticket package with a secret of at least 32 characters and a lifetime of 30 seconds to five minutes.
5. In the same Neon transaction as each durable message/post write, insert an outbox event. Never publish before commit.
6. Add a publisher that signs the exact raw event body with a separate realtime publish secret and retries until Cloudflare acknowledges.
7. Add a resumable Appwrite-to-Neon exporter/importer with stable checkpoint, idempotency, row-count/hash reconciliation and rollback rehearsal.
8. Move the feed reader and Federation outbox reader to Neon before removing corresponding Appwrite collections.

## Data and identity safety

- Appwrite account IDs remain canonical immutable user IDs.
- Do not use email addresses as primary keys.
- Do not silently create replacement identities.
- No source Appwrite documents are modified or deleted by this branch.
- No production traffic, credentials or database routing are changed.
- Media ownership and B2/R2 replica ledger remain behind Backend/Data Centre contracts; do not store media bytes in Neon.
- Retention and destructive deletion remain disabled.
