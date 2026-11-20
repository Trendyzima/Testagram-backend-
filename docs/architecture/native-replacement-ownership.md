# Testagram-native replacement ownership

## Production entrypoint

The only supported public API runtime is the Testagram Go service assembled by cmd/api/main.go. It owns native PostgreSQL account/session validation, identity intake and provisioning, staff authorization, profile/social APIs, media metadata and service orchestration. No retired external identity provider is used by this request path.

## Retired runtime components and replacements

| Retired component | Retired responsibility | Testagram-owned replacement |
|---|---|---|
| Legacy document database client | Document CRUD, transactions and provider credentials | PostgreSQL pool, native repositories and transactional outboxes |
| Parallel legacy API server | Separate auth middleware and route tree | Production router in cmd/api/main.go |
| Legacy identity adapter | Account signup/linking through external credentials | cmd/api/auth.go, cmd/api/native_sessions.go and cmd/api/identity_*.go |
| Legacy media lifecycle package and commands | Quota reservations, document-backed media metadata, retention leases and replica records | cmd/api/media.go, PostgreSQL media/storage schemas, internal/replication and native storage workers |
| Legacy deployment profile | Provider endpoint/project/key configuration | deploy/self-hosted and Dockerfile production API image |
| Legacy API description and validator | Described obsolete route/auth contract | Canonical API contracts in Trendyzima/testagram-api and scripts/validate-native-api-contract.py |

## Native authentication contract

- User bearer tokens are issued by Testagram and checked against live PostgreSQL account/session state.
- Refresh credentials are opaque, one-time-use and rotated; replay revokes the token family.
- Invalid native credentials fail closed. There is no external provider selector or fallback.
- Staff access requires enabled native staff principals, server-side role grants and the configured backend-verified WebAuthn/MFA elevation.
- Identity intake does not create an account. Provisioning requires completed evidence review, independent dual control and a consumed one-time native authorization.
- Email verification and account activation are separate mandatory gates.

## Native media contract

- PostgreSQL owns upload intents, owner/visibility, quotas, checksums, assets, replica state and durable deletion jobs.
- The API claims upload intents before accepting bytes and computes the authoritative SHA-256.
- Replication and deletion are durable, retryable operations. A queue event is not proof of a verified replica or completed deletion.
- CDN transports bytes but never authorizes access. Private media requires Backend authorization and only verified placements are eligible.
- The old standalone story-expiry/cold-archive worker suite is not represented as complete by the native upload/deletion pipeline. Any future retention feature must use native ledgers and pass restore, legal-hold and destructive-operation acceptance.

## Legacy data and migration history

Previously applied migration files and the archived one-time import tool may retain old provider identifiers as historical source metadata. They are not imported by the production binary, startup, CI deployment or authentication path. Keep them isolated for audit/data lineage until a database inventory proves their data can be safely retired.

The forward-only archive migrations move old account mapping, link audit, registration consent and provisioning tables into `testagram_identity.historical_external_*`; provider-keyed staff roles, permission overrides, sensitive-action requests/approvals and admin audit tables move into `testagram_auth.historical_external_*`. Source identifiers are renamed to neutral source-account/staff identifiers and runtime-role privileges are revoked. Existing review and approval foreign keys remain attached to the historical staff tables so audit lineage is not broken. New authorization reads only `testagram_auth.staff_principals`, `staff_role_grants` and `staff_role_permissions`; the Admin Console writes only `native_sensitive_action_*`, `native_staff_permission_overrides` and `native_admin_security_audit`. New registration provisioning reads only `testagram_identity.native_registration_provisioning`.

Never rewrite or delete an applied migration. Any database cleanup must be a new forward-only migration after checking live rows, foreign keys, triggers, indexes, stored procedures, grants and backup/restore behavior.

## Acceptance checks

- go test -race -count=1 ./...
- go vet ./...
- CGO_ENABLED=0 go build ./cmd/api ./admin-console
- scripts/validate-native-api-contract.py
- PostgreSQL integration for signup, identity review, dual-control provisioning, email verification, login, refresh rotation, logout and staff WebAuthn.
- Media upload, checksum, replication, private CDN authorization and deletion integration tests.
- Production smoke tests proving that only the native route contract is served and no outbound identity-provider traffic occurs.
