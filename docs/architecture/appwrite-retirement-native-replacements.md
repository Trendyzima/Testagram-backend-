# Appwrite Retirement: Native Replacement Map

## Purpose

This document records the production-owned replacements for every retired Appwrite runtime file. The old `internal/freshapi.Server` was a parallel legacy server and was not constructed by the production `cmd/api/main.go` entrypoint. Do not recreate that server or restore Appwrite HTTP calls under a new package.

## File-by-file replacement map

| Retired file | Retired responsibility | Testagram-owned replacement |
|---|---|---|
| `internal/appwritedb/client.go` | Appwrite REST database, document and transaction operations | PostgreSQL pool and repositories owned by `cmd/api`; identity and social persistence are implemented in the native API handlers and migrations |
| `internal/appwritedb/client_test.go` | Tests for Appwrite REST client behavior | Native PostgreSQL integration and API tests, including `cmd/api/native_sessions_integration_test.go`, `cmd/api/identity_registration_http_integration_test.go`, `cmd/api/social_postgres_integration_test.go` and related native tests |
| `internal/freshapi/server.go` | Parallel Appwrite-authenticated API server and legacy route registration | Production router in `cmd/api/main.go`; native auth in `cmd/api/auth.go` and `cmd/api/native_sessions.go`; native identity in `cmd/api/identity_native.go`; profile routes in `cmd/api/social_profile.go` and `cmd/api/social_profile_update.go`; federation routes in `cmd/api/federation_api.go` |
| `internal/freshapi/server_test.go` | Tests for the retired parallel server and Appwrite-backed routes | Production API tests in `cmd/api/*_test.go`, especially native auth, identity registration, profile, federation and health integration tests |
| `internal/freshapi/identity.go` | Legacy Appwrite identity-registration adapter | Testagram-owned identity application and provisioning pipeline in `cmd/api/identity_native.go`, `cmd/api/identity_provisioning.go` and `cmd/api/identity_native_provisioning.go` |
| `internal/freshapi/identity_test.go` | Tests for the retired Appwrite identity adapter | Native identity registration, provisioning, review and HTTP integration tests under `cmd/api/` |
| `internal/freshapi/moderation_test.go` (legacy Appwrite-coupled test section) | Tests that depended on the retired server adapter | Keep moderation implementation and its provider tests in `internal/freshapi/moderation.go` and the remaining `internal/freshapi/moderation_test.go`; production moderation is wired by `cmd/api/main.go` |

## Active native route ownership

The production router in `cmd/api/main.go` owns the API routes. Relevant route families include:

- `/api/auth/*`: signup, email verification, login, refresh, logout, password recovery.
- `/api/identity/applications/*` and `/api/identity/sessions/*`: identity application intake and verification.
- `/api/me/profile` and `/api/social/profiles/*`: native profile reads and updates.
- `/api/admin/identity/*`: staff identity review.
- `/internal/v1/federation/*`: internal federation actor, object and subscription operations.

The old `/v1/identity/register` and `/v1/me` legacy routes are not preserved as Appwrite compatibility aliases. Clients must use the production native route contract.

## Security and migration constraints

1. No Appwrite SDK, HTTP endpoint, API key, JWT, cookie or session may be accepted by the production runtime.
2. Native bearer authentication must validate Testagram-issued tokens against live PostgreSQL account and session state.
3. Refresh tokens remain opaque, rotating, one-time-use credentials; refresh reuse revokes the token family.
4. Identity approval and staff MFA/WebAuthn gates must remain enforced. Do not weaken a production registration gate merely to make signup appear successful.
5. Historical Appwrite migration/audit SQL and reconciliation documentation are retained only where needed for data lineage and migration safety. They are not runtime dependencies.
6. Do not delete or rewrite applied migration history as part of runtime retirement.

## Required acceptance checks

- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- `go build ./...`
- PostgreSQL integration tests for native signup/provisioning, login, refresh rotation, logout, profile persistence and identity review.
- Search all tracked runtime files, workflows, deployment manifests and environment templates for `appwrite`, `APPWRITE_`, `X-Appwrite-` and Appwrite endpoint URLs.
- Confirm all seven repositories have no active Appwrite runtime, build dependency, secret, workflow or client configuration.
- Confirm the deployed API serves the native route contract and does not route traffic to the retired server.

A pull request is not accepted until required CI checks pass and the deployment smoke tests verify the live service.
