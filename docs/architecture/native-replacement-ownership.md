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

The forward-only archive migrations (`058a` through `058d`) move old account mapping, link audit, registration consent and provisioning tables into `testagram_identity.historical_external_*` when present; provider-keyed staff roles, permission overrides, sensitive-action requests/approvals and admin audit tables move into `testagram_auth.historical_external_*`. Source identifiers, provider-branded constraint/index names and native reviewer-attribution columns are renamed to neutral historical names; the old provider-specific provisioning state is normalized to `source_provider_created`. The obsolete `identity_applications.sensitive_action_request_id` link and its foreign key/index are renamed into the historical namespace; new approvals use only `native_sensitive_action_request_id`. Runtime-role privileges on historical tables are revoked. Existing review and approval foreign keys remain attached to historical staff tables so audit lineage is not broken. Historical data is retained, not automatically converted into native staff grants or used as account-ownership proof. New authorization reads only `testagram_auth.staff_principals`, `staff_role_grants` and `staff_role_permissions`; the Admin Console writes only `native_sensitive_action_*`, `native_staff_permission_overrides` and `native_admin_security_audit`. New registration provisioning reads only `testagram_identity.native_registration_provisioning`. The three original provider-named migration files remain unchanged because they are applied migration history; clean installs do not mount them, and the archive integration test covers both present/absent legacy tables and reapplies the migrations twice.

Never rewrite or delete an applied migration. Any database cleanup must be a new forward-only migration after checking live rows, foreign keys, triggers, indexes, stored procedures, grants and backup/restore behavior.

## Acceptance checks

- go test -race -count=1 ./...
- go vet ./...
- CGO_ENABLED=0 go build ./cmd/api ./admin-console
- scripts/validate-native-api-contract.py
- PostgreSQL integration for signup, identity review, dual-control provisioning, email verification, login, refresh rotation, logout and staff WebAuthn.
- Media upload, checksum, replication, private CDN authorization and deletion integration tests.
- Production smoke tests proving that only the native route contract is served and no outbound identity-provider traffic occurs.


## Native mail is the only supported mail architecture

Testagram owns transactional email, account-verification OTPs, password-recovery messages, notification mail, delivery state and feedback processing. Production mail is sent by the Testagram Backend's native direct-to-recipient-MX transport from the InstaBox deployment. It is not delegated to a hosted email API or an authenticated relay account.

### Permanently retired configuration

The following names are historical/forbidden configuration identifiers. They must not be added to application code, deployment scripts, Compose files, workflow environments, GitHub Actions secrets/variables, VPS environment templates, operator instructions, examples, tests that configure runtime values, or any new service contract:

| Retired identifier | Retirement reason | Required native behavior |
|---|---|---|
| `MAILJET_API_KEY` | Hosted Mailjet API credential | No external mail-provider API or API key |
| `MAILJET_SECRET_KEY` | Hosted Mailjet API credential | No external mail-provider API or API secret |
| `SMTP_HOST` | Configurable third-party SMTP relay endpoint | Resolve recipient MX records and connect directly to recipient MX on TCP/25 |
| `SMTP_USERNAME` | Authenticated relay account | No SMTP AUTH; do not introduce relay credentials |
| `SMTP_PASSWORD` | Authenticated relay password | No SMTP AUTH; do not introduce relay credentials |
| `AUTH_FROM_EMAIL` | Legacy sender-address override | Use the code-owned sender identity `no-reply@testagram.site`; changes require an explicit reviewed architecture decision and coordinated DNS/DKIM/DMARC update |

The retired names may appear in this architecture document and in negative CI assertions solely to explain and enforce the prohibition. They must not appear as active settings, sample values, workflow expressions, secrets, variables, or runtime fallbacks.

### Supported native mail contract

- The Backend's PostgreSQL transactional outbox is the durable source of pending mail work. Messages are encrypted at rest with the native mail encryption key; delivery attempts, leases, uncertainty, terminal failures and delivery events are recorded in the native mail schema.
- The InstaBox deployment generates and persists the native encryption and VERP keys on the VPS. Keep the keys stable across restarts and deployments; never regenerate them on every deploy, commit them, print them to logs, or move their values into chat.
- The sender resolves the recipient domain's MX records and connects directly to those MX hosts on TCP/25. STARTTLS is required. SMTP AUTH is not used. A network that blocks outbound TCP/25 is a hard delivery blocker, not a reason to silently fall back to a provider.
- The sender identity is code-owned as `no-reply@testagram.site`. DKIM signing uses the Testagram-owned private key and selector. SPF, DKIM, DMARC, MX/bounce routing, reverse DNS/PTR and forward-confirmed identity must be validated against live DNS and actual delivery evidence.
- VERP return paths and the native bounce-feedback endpoint correlate bounces to outbox records. Outbound delivery must remain fail-closed until the required bounce ingress readiness checks pass. Do not bypass that gate to make the readiness probe green.
- A successful local queue insert or SMTP acceptance is not proof of inbox delivery. Acceptance requires real external mailbox tests, authentication/alignment checks, bounce and complaint handling, retry/idempotency tests, restart/key-persistence checks and operational monitoring.
- Native email verification is one gate in account activation; it must not be conflated with identity-evidence review, staff enrollment or administrative authorization.

### New-service and future-build rules

1. Before adding any email feature, inspect and extend the existing native mail outbox, templates, consent policy, transport and feedback pipeline. Do not create a second mail subsystem.
2. Do not add a vendor SDK, hosted email API, SMTP relay account, provider-specific webhook, provider credentials, or environment-variable fallback. Any proposal to change this architecture requires an explicit owner-approved architecture decision recorded here before implementation.
3. Do not add compatibility aliases for retired settings. Missing native configuration must fail closed with a clear diagnostic; it must never activate a legacy transport.
4. Keep secrets in protected deployment storage. The native encryption/VERP/DKIM material is provisioned and persisted on InstaBox according to the reviewed deployment contract; never copy secret values into repository files or CI logs.
5. Add regression tests whenever mail configuration, delivery, DNS, deployment or feedback code changes. CI must reject the retired names in runtime/deployment surfaces and verify that direct-MX delivery, STARTTLS, no-auth transport and native key persistence remain intact.
6. Historical references in this document and negative tests are policy evidence, not permission to restore the old integrations.

### Production readiness gates

Native mail is production-ready only after all of the following have passed with evidence: external Gmail and Outlook delivery; SPF/DKIM/DMARC alignment; correct PTR and forward DNS; TCP/25 reachability from the actual InstaBox machine; bounce ingress authentication and correlation; suppression/consent behavior; duplicate/replay and retry safety; uncertain SMTP outcome handling; encrypted outbox/key persistence across restart and restore; and monitoring/alerting for queue age, dead letters, bounce rates and delivery failures. A dry-run build, DNS record presence, or secret-name listing alone does not satisfy these gates.
