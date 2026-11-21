# Historical five-repository integration note

This file is retained only as a historical pointer. Its earlier ownership and route assumptions have been superseded and must not be used for implementation, deployment or client integration.

## Current authoritative contracts

- [Six-repository runtime integration contract](six-repository-runtime-integration-contract.md) defines current cross-repository ownership and service boundaries.
- [Testagram-native replacement ownership](architecture/native-replacement-ownership.md) defines the Backend's production runtime and native subsystem owners.
- [Native authentication acceptance](platform/native-auth-acceptance.md) defines signup, email verification, login, refresh rotation, logout and staff authentication gates.

## Current identity boundary

Flutter uses the Testagram Backend's native authentication API. The Backend owns account creation, password verification, email verification, session issuance and revocation, canonical Testagram UUIDs, authorization and PostgreSQL identity state. Internal services receive scoped service credentials; they do not validate user sessions or become alternate identity authorities.

Do not use the former five-repository route map or its retired identity-provider assumptions as a current API contract. Verify routes against the production router and the current OpenAPI contract before changing a client.
