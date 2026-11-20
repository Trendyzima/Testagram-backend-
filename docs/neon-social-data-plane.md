# Historical social data-plane proposal

This document records an earlier database-placement proposal. It is retained for audit context only; it does not define the current production database, identity provider, deployment routing or migration plan.

## Current Testagram ownership

- Testagram Backend owns native account identity, sessions, authorization, profiles, posts, social relationships, messages, notifications and transactional outboxes.
- The canonical production PostgreSQL database is deployed with the Backend on the Instabox VPS.
- Federated Content owns ActivityPub protocol state in its separately permissioned PostgreSQL database and asks Backend to authorize local actors/content.
- The Algorithm receives bounded, already-authorized ranking candidates and returns ordered IDs; Backend rechecks visibility and hydrates canonical records.
- Data Centre owns deployment, private networking, storage nodes, media durability, backups and recovery.
- Realtime delivery is downstream of durable Backend writes. It is not the canonical store for posts or messages.

## Capacity and traffic assumptions

The historical planning workload was 1.5 million database writes/month and 15 million API requests/month. Those averages are not peak-capacity estimates. Production acceptance requires burst tests, connection-pool tests, query plans, storage-growth measurement and a realistic realtime concurrency profile.

## Current migration safety

Do not move canonical production data or change database routing based on this proposal. Any future data-plane change requires:

1. a reviewed target architecture and least-privilege database roles;
2. additive, ordered migrations with rollback and backup/restore evidence;
3. stable checkpoints and idempotent import/export;
4. row counts, IDs, relationships, timestamps, visibility and media-reference reconciliation;
5. dual-read comparisons without stale or unauthorized responses;
6. a controlled write cutover with replayable change capture;
7. a rehearsed rollback and explicit approval before destructive cleanup;
8. API, federation, messaging and realtime load/security tests.

Canonical user identity is the Testagram UUID. Do not use email as a primary key or silently create a replacement identity during a data migration.

## Current runtime contract

Use the [six-repository runtime integration contract](six-repository-runtime-integration-contract.md), [native authentication acceptance](platform/native-auth-acceptance.md) and [native replacement ownership map](architecture/native-replacement-ownership.md) for current ownership, service boundaries and release gates.
