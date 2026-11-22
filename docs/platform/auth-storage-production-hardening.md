# Native authentication and production hardening gates

This document separates the current mainline implementation from the deployed production state. It is a release gate, not production cutover authorization.

## Current source implementation

- Flutter mainline uses the Testagram-native auth client for login, refresh, recovery/reset, session restoration and logout.
- Backend mainline owns canonical credentials and session state in PostgreSQL. New password hashes use Argon2id; access JWTs live 15 minutes; opaque refresh tokens are hashed, rotate on use and expire after 30 days.
- Protected requests recheck the live session, token version, account status, email verification and provisioning state.
- Password recovery is anti-enumerating and single-use; reset revokes existing sessions.
- Admin Console staff login uses native Backend sessions and an encrypted `testagram-staff` cookie. Staff role grants and an MFA-enrollment marker are checked. Current step-up is password re-authentication only; TOTP/passkey verification and staff onboarding remain incomplete and closed.
- Data Centre user-aware video/notification routes verify the same native access JWT and query the live PostgreSQL session/account state. The Data Centre platform API uses a separate internal service token.

## Historical identity records

Historical external-provider identity mappings and audit rows are retained under the restricted `testagram_identity.historical_external_*` namespace. The offline reconciliation tool is `scripts/archive/legacy-identity-provider-import.sql`; it requires explicit source-account-to-canonical-UUID mappings, does not import source passwords and never matches accounts by email alone. Reconciled accounts must complete native password reset before using Testagram credentials. Historical source identifiers are not accepted as runtime credentials, and the production API has no external-session fallback.

## Required deployment configuration

- Apply all native-auth, identity-provisioning, staff-role and runtime-grant migrations in order.
- Configure a high-entropy `TESTAGRAM_AUTH_JWT_SECRET` of at least 32 random bytes in Backend and every user-aware Data Centre service. These values must match exactly; do not expose them to Flutter, CDN or public docs.
- Configure PostgreSQL connection, required grants, rate-limit storage and backups.
- Configure and verify the Testagram email transport, sender domain and `AUTH_PUBLIC_BASE_URL` for verification and password-reset links.
- Confirm Backend and Flutter use the same production API origin and that the deployed Flutter build actually contains the native auth client.
- Verify the Admin Console cookie encryption key and HTTPS-only cookie settings.

## Mandatory staging acceptance

1. Create a dedicated test identity through the real application flow; do not bypass the review/approval ledger.
2. Prove two independent reviewers and two independent approvers are required before native provisioning.
3. Receive a real verification email, accept its single-use token and prove sign-in is rejected before verification.
4. Sign in with the native password, restart the Flutter app, restore via refresh, and verify the Backend profile before opening the signed-in shell.
5. Rotate refresh tokens; prove old-token replay revokes the family and that logout/logout-all revoke the expected sessions.
6. Request password recovery for existing and nonexistent addresses and confirm the public response is indistinguishable; receive and consume a real reset link; prove prior sessions are revoked.
7. Verify disabled, unverified, pending-provisioning, reset-required and revoked-session access is denied.
8. Verify Data Centre video/notification routes accept a valid native session and reject revoked, expired, disabled and unverified sessions.
9. Verify private media still requires Backend viewer assertion and separate CDN origin authentication; never forward the user JWT as an origin service credential.
10. Verify staff sign-in, staff role checks, real second-factor challenge, step-up, logout and two-person role/onboarding gates before opening staff enrollment.

## Production boundary

The source implementation and CI results do not prove which auth provider is currently running in production. Do not disable any legacy production provider, switch production auth, enable public registration, activate accounts, alter routing or deploy based on this document alone. Require exact-head CI, independent review, real staging evidence and separate explicit production-release approval.
