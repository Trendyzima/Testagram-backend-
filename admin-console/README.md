# Testagram Staff Console

The staff console uses Testagram-native PostgreSQL identities and short-lived
Testagram bearer sessions. It has no external identity-provider dependency.

## Required configuration

- `BACKEND_INTERNAL_URL` is the private Backend API origin (normally
  `http://api:8080` on the private Docker network).
- `TESTAGRAM_AUTH_JWT_SECRET` must exactly match the Backend's native JWT
  signing secret. It is server-side only and must never be exposed to browsers.
- `DATABASE_URL` points to the canonical Testagram PostgreSQL database.
- `ADMIN_SESSION_KEY` is 64 hexadecimal characters generated from 32 random
  bytes. It encrypts the HttpOnly staff-session cookie.
- `ADMIN_PUBLIC_URL` must be the exact HTTPS origin routed to this service.
- `ADMIN_SERVICE_AUTH_KEY` is an optional independent 32-byte internal
  service credential. It must not be reused as the JWT or cookie key.

## Native authentication and session handling

1. Staff sign in through `POST /api/auth/login` on the private Backend API.
2. The console stores the short-lived access token and rotating refresh token
   only inside an AES-GCM encrypted, HttpOnly, Secure, SameSite=Strict cookie.
3. Before the access token expires, the console rotates the refresh token
   through `POST /api/auth/refresh`; revoked or invalid sessions fail closed.
4. Each request verifies the JWT signature and claims, then confirms the
   canonical PostgreSQL session, token version, account status, enabled native
   staff principal, active role grant and enrolled MFA.
5. Identity-review requests forward the native bearer token to Backend.
   Backend independently checks the staff principal, role and MFA before
   returning queue data or private evidence.
6. Password recovery and reset use Backend's native
   `/api/auth/forgot-password` and `/api/auth/reset-password` endpoints.
7. Sign-out revokes the native Backend session and clears the encrypted cookie.

Supported staff roles: owner, administrator, engineer, operations, support and
moderator. Role and permission values are never accepted from browser fields.

## Staff provisioning and role changes

Staff principals are stored in `testagram_auth.staff_principals` and
`testagram_auth.staff_role_grants`. A principal is eligible only when enabled,
has an active approved role grant and has enrolled MFA.

The console deliberately does not create staff identities or mutate role
assignments directly. The two-person native staff provisioning workflow must
create/enable the principal and grant/revoke roles with independent approvals.
Until that workflow is complete, invitation, role-change and revocation
mutations fail closed. Do not manually insert an owner or bypass the approval
ledger to make the dashboard accessible.

The owner-only staff directory is read from canonical UUID identities and
native staff grants. Permission overrides use
`testagram_auth.native_staff_permission_overrides`. Sensitive-action requests,
approvals and audit entries use the native UUID ledgers created by
`migrations/20261118_native_admin_console.sql`.

## Database migrations

Fresh self-hosted database bootstrap mounts migration 054 after native staff
reviewer grants. Existing persistent databases do not replay
`docker-entrypoint-initdb.d`; apply migrations 053 and 054 through the
controlled operator migration process, inspect the resulting schema and
verify grants before deploying this console.

Legacy provider-keyed staff rows are not automatically linked to canonical
Testagram identities. Preserve them for reconciliation/audit; do not map
accounts by email alone or grant staff access without independent verification
and two-person approval.

## Security and production gates

- Keep the console on the private edge route and enforce exact-origin POSTs.
- Keep the JWT secret, database credentials and cookie encryption key out of
  browser assets and logs.
- Verify refresh rotation, session revocation, account disablement and password
  reset invalidation.
- Verify unapproved, disabled and non-MFA staff principals receive 403.
- Verify Backend independently rejects identity-review requests without a
  native staff session, active reviewer grant and enrolled MFA.
- Verify evidence remains private and is streamed only through authenticated
  Backend review routes.
- The console's approval ledger records decisions; it does not itself execute
  financial, security or deployment actions. Those services must independently
  validate approved request hashes, permissions, target state and idempotency.
- Do not enable production identity, finance, security or deployment actions
  until the two-person staff provisioning flow, phishing-resistant MFA policy,
  operation adapters and end-to-end acceptance tests pass.
