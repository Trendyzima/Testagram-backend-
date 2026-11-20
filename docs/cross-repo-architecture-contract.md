# Historical cross-repository architecture note

This document is retained only as a historical pointer. Its earlier repository count, identity ownership and service-flow assumptions have been superseded. Do not use it to infer current deployment wiring, authentication behavior, migration sources or runtime dependencies.

## Current authoritative contracts

- [Six-repository runtime integration contract](six-repository-runtime-integration-contract.md) defines the six runtime repositories, separate API contract control plane, service ownership and request paths.
- [Testagram-native replacement ownership](architecture/native-replacement-ownership.md) defines Backend runtime ownership and native subsystem replacements.
- [Native authentication acceptance](platform/native-auth-acceptance.md) defines signup, email verification, login, refresh rotation, logout and staff authentication gates.
- [Virtual storage contract](platform/virtual-storage.md) defines native media storage and lifecycle boundaries.
- [Replication transport contract](dual-backend-replication-transport.md) defines durable, authenticated, idempotent event delivery.

## Current identity and authorization boundary

Flutter obtains native Testagram access and refresh credentials from Backend. Backend owns canonical user UUIDs, password verification, email verification, session issuance/revocation, product authorization and PostgreSQL state. Algorithm, Data Centre, CDN and Federated Content receive only scoped service contracts; none may issue user sessions or override Backend authorization.

## Migration safety

Preserve applied migration history. Use additive, ordered migrations, explicit ownership mappings, idempotent reconciliation, tombstones, least-privilege roles and tested backup/restore. Never infer account ownership from email equality, mirror health or a successful transport acknowledgement. Do not restore superseded routes, credentials or data-source fallbacks from this document.
