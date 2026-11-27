# Testagram-native identity, sessions and media ownership

## Runtime authority

The production API is the Go service assembled by `cmd/api/main.go`. Testagram's PostgreSQL schemas are authoritative for accounts, sessions, identity applications, staff principals, authorization, consent audit and media metadata. No external identity provider is called to authenticate users or staff.

- User authentication and rotating refresh sessions: `cmd/api/auth.go` and `cmd/api/native_sessions.go`.
- Identity intake, verification and evidence: `cmd/api/identity_native.go` and the `cmd/api/identity_*.go` provisioning/review handlers.
- Staff authorization and sensitive-action approval: native staff principals, role grants, dual-control ledgers and WebAuthn credentials in `testagram_auth`.
- Profiles and social graph: `cmd/api/social_profile.go`, `cmd/api/social_profile_update.go` and native social handlers.
- Media ownership and lifecycle: `cmd/api/media.go`, `cmd/api/fresh_media_bridge.go`, PostgreSQL asset/replication ledgers and the storage workers.
- Federation publication: PostgreSQL-backed `internal/federationoutbox`; local accounts and canonical posts remain Backend-owned.

## Authentication invariants

1. The only accepted user bearer is a Testagram-issued short-lived access token validated against live account and session state.
2. Refresh credentials are opaque, rotated, one-time-use values. Reuse revokes the affected token family.
3. Native auth failure fails closed. There is no provider selector, token translation, compatibility fallback or provider-specific cookie.
4. Staff access requires an enabled Testagram staff principal, server-side permission checks and the required backend-verified WebAuthn/MFA elevation.
5. Identity registration and provisioning are distinct states. Intake does not create an account; provisioning requires completed evidence review, independent two-person approval, unconsumed authorization and native email verification.
6. Private identity evidence remains in the private evidence-storage lifecycle. Database rows contain opaque references and digests, not raw identity documents.

## Native route contract

The route source of truth is the production router in `cmd/api/main.go`. Key route families include:

- `POST /api/auth/signup`, `/verify-email`, `/login`, `/refresh`, `/logout`, `/forgot-password`, `/reset-password`.
- `POST /api/identity/applications` and `GET /api/identity/applications/{application_id}`.
- `GET /api/identity/sessions/{session_id}` plus evidence upload and submit routes.
- `GET /api/admin/identity/review-queue`, evidence review and decision routes.
- `POST /api/identity/applications/{application_id}/provisioning-token` and native redemption/verification routes.
- `GET/PUT /api/me/profile` and `GET/PUT /api/me/federation`.

Do not restore old routes as aliases without proving a current client dependency and adding contract tests. In particular, intake and account provisioning must not be conflated, and an old registration URL must not bypass evidence, review or approval gates.

## Storage and delivery boundaries

The Backend owns identity upload authorization and reviewer access. Identity-evidence bytes and their sidecars live only in the dedicated private Backblaze B2 identity bucket managed by the Data Centre; they are not sent to InstaBox, Cloudflare R2, the general-media replication outbox or the CDN. The Data Centre verifies the B2 object and fails closed on provider failure or checksum mismatch. Separately, ordinary media may use the InstaBox VPS, Cloudflare R2 and Backblaze B2 general-media topology. The CDN transports authorized ordinary media bytes but never grants identity-evidence access. Credentials remain server-side.

## Historical identity records

Some already-applied migrations created provider-specific columns and tables for a previous identity design. They are retained as historical data lineage and must not be treated as runtime dependencies. A forward-only schema migration may rename legacy identifiers to neutral historical-provider names after checking every application query, foreign key, trigger, index, report and import tool. Never rewrite or delete an applied migration file, and never discard verified account-link or consent audit rows as part of code retirement.

## Release acceptance

- Run `go test ./...`, race tests, `go vet ./...` and `go build ./...`.
- Run PostgreSQL integration tests for signup/intake, evidence, two-person review, native provisioning, email verification, login, refresh rotation, logout, staff WebAuthn and authorization denial.
- Run media lifecycle and federation outbox integration tests.
- Search all tracked source, deployment manifests, workflow files, secrets templates and client configuration for provider endpoints, keys, headers, token formats and runtime toggles.
- Test the deployed API route contract and verify there is no outbound identity-provider traffic.
