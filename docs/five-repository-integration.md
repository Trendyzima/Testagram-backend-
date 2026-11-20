# Historical five-repository integration note

This file is retained only as a historical pointer. Its earlier identity, repository-count and service-flow assumptions have been superseded. Do not use it to infer current deployment wiring or client authentication behavior.

## Current authoritative contracts

- [Six-repository runtime integration contract](six-repository-runtime-integration-contract.md) defines current repository ownership and service-to-service boundaries.
- [Testagram-native replacement ownership](architecture/native-replacement-ownership.md) defines the production Backend runtime and subsystem owners.
- [Native authentication acceptance](platform/native-auth-acceptance.md) defines the user and staff authentication gates.

## Current identity and service boundaries

The Flutter client uses the Testagram Backend's native authentication API. The Backend owns account creation, email verification, password login, refresh rotation, logout, canonical Testagram UUIDs, authorization and PostgreSQL identity state. Data Centre, Algorithm and Federated Content receive only scoped service-to-service contracts; they do not become alternate user identity authorities.

Use the production router and current OpenAPI contract for route decisions. Do not restore routes, credentials or migration behavior from the superseded five-repository design.
