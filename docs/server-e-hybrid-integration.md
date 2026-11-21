# Historical hybrid integration note

This file is retained only as a historical pointer. Its earlier repository count, identity ownership and service-flow assumptions have been superseded. Do not use it as a current runtime, authentication or deployment contract.

## Current authoritative contracts

- [Six-repository runtime integration contract](six-repository-runtime-integration-contract.md) defines the current runtime repositories, service ownership and communication paths.
- [Testagram-native replacement ownership](architecture/native-replacement-ownership.md) defines the Backend's native subsystem owners.
- [Native authentication acceptance](platform/native-auth-acceptance.md) defines the current user and staff authentication gates.
- [Testagram service ownership catalogue](platform/service-catalog.md) summarizes the current service boundaries.

## Current identity boundary

Flutter obtains native Testagram access and refresh credentials from Backend. Backend owns account creation, password verification, email verification, session issuance and revocation, canonical Testagram UUIDs, authorization and PostgreSQL identity state. Internal services receive scoped service-to-service credentials only; they do not issue user sessions or validate user bearer credentials as a separate identity authority.

## Current release rule

Use the production router and shared API contract control plane for route decisions. Verify the exact coordinated repository commits, database migrations, authorization tests, media lifecycle tests, federation contract tests, backup/restore evidence and deployed staging acceptance before production cutover. This historical note does not authorize enabling a feature flag or changing production routing.
