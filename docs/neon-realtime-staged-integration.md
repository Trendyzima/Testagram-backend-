# Historical staged realtime prototype

This document records an earlier staged data-plane proposal. It is not the current production deployment or identity contract and must not be used to change live database routing, credentials or account ownership.

## Current ownership

- Testagram Backend owns native account identity, sessions, authorization, PostgreSQL product records and transactional outboxes.
- The canonical production PostgreSQL database is deployed with the Backend on the Instabox VPS.
- Realtime delivery is downstream of Backend authorization and durable database commits. It must never become an independent user-identity or product-data authority.
- Media bytes and physical replica placement remain under the Backend/Data Centre media contracts.

## Current safety requirements

1. Authenticate users through Testagram Backend native sessions and resolve canonical Testagram UUIDs from PostgreSQL.
2. Check conversation membership, blocks and privacy rules in Backend before issuing any short-lived realtime capability.
3. Write durable messages and outbox events in the same PostgreSQL transaction; never publish before commit.
4. Sign exact event payloads with a distinct service credential and retry delivery idempotently.
5. Keep pagination keyset-based and enforce bounded message payloads, rate limits and membership rechecks.
6. Preserve audit history and require verified backups/restore before any retention or destructive deletion rollout.

Do not use this historical prototype as authorization to migrate production identity, alter canonical database placement or remove existing records. The current service contract and release gates are maintained in the [six-repository runtime integration contract](six-repository-runtime-integration-contract.md).
