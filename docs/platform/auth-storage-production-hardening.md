# Native authentication and production hardening gates

This document separates the current source implementation from deployed production state. It is a release gate, not production cutover authorization.

## Current source implementation

- Flutter uses the Testagram-native auth client for ordinary signup, email verification, login, refresh, recovery/reset, session restoration and logout.
- Backend owns canonical credentials and session state in PostgreSQL. New password hashes use Argon2id; access JWTs live 15 minutes; opaque refresh tokens are hashed, rotate on use and expire after 30 days.
- Public signup creates an unverified basic account and queues a one-time email verification code/link. Identity evidence review and two-person approval are not prerequisites for ordinary account creation or email verification.
- Protected requests recheck the live session, token version, account state, email verification and any explicitly applicable privilege/assurance requirements.
- Password recovery is anti-enumerating and single-use; reset revokes existing sessions.
- Staff and identity-review capabilities remain separate privileged workflows with their own roles, MFA/WebAuthn, authorization and approval controls.
- Data Centre user-aware video/notification routes verify the same native access JWT and query live PostgreSQL session/account state. The Data Centre platform API uses a separate internal service token.

## Registration and identity-assurance boundary

Ordinary users must be able to register and use basic Testagram after email verification without submitting identity documents or waiting for staff review. This does not confer verified-identity status, staff access, administrator permissions or other elevated capabilities.

The identity-evidence workflow is optional and independently gated. Its WebAuthn reviewer/approver readiness must not be a prerequisite for deploying the Backend, enabling the ordinary signup route, verifying email or signing in. The deployment workflow defaults `TESTAGRAM_IDENTITY_REVIEW_WEBAUTHN_ENABLED` to false; enabling it requires an explicit production variable and its own complete acceptance. Never change this gate merely to make ordinary account creation work.

## Historical identity records

Historical external-provider identity mappings and audit rows are retained under the restricted `testagram_identity.historical_external_*` namespace. The offline reconciliation tool is `scripts/archive/legacy-identity-provider-import.sql`; it requires explicit source-account-to-canonical-UUID mappings, does not import source passwords and never matches accounts by email alone. Reconciled accounts must complete native password reset before using Testagram credentials. Historical source identifiers are not accepted as runtime credentials, and the production API has no external-session fallback.

## Required deployment configuration

- Apply native-auth, mail-outbox, identity-assurance, staff-role and runtime-grant migrations in order.
- Configure a high-entropy `TESTAGRAM_AUTH_JWT_SECRET` of at least 32 random bytes in Backend and every user-aware Data Centre service. These values must match exactly; do not expose them to Flutter, CDN or public docs.
- Configure PostgreSQL connection, required grants, rate-limit storage and backups.
- Configure and verify the Testagram mail transport, sender domain and HTTPS `AUTH_PUBLIC_BASE_URL` for verification and password-reset links.
- Keep `TESTAGRAM_MAIL_ENCRYPTION_KEY` stable. The deployment script generates and persists a 32-byte/64-hex key on the VPS when absent and verifies that the API and Admin Console receive the same key. Losing or rotating it without a planned queue migration makes existing encrypted pending mail unreadable.
- Confirm Backend and Flutter use the same canonical production API origin and that the deployed Flutter build contains the native auth client.
- Verify Admin Console cookie encryption and HTTPS-only cookie settings.
- Do not require identity-review WebAuthn readiness for an ordinary Backend deployment.

## Mandatory staging acceptance

1. Create a fresh ordinary account through the real Flutter signup page against the real Backend and isolated PostgreSQL database.
2. Confirm the account is unverified and login is denied until email verification.
3. Receive the actual verification email in a controlled mailbox; verify the one-time link and six-digit OTP paths and confirm replay is rejected.
4. Confirm successful verification initializes the basic local profile and enables ordinary sign-in without identity evidence, reviewer approval or two-person provisioning.
5. Restart the Flutter app, restore through refresh, and verify the Backend profile before opening the signed-in shell.
6. Rotate refresh tokens; prove old-token replay revokes the family and that logout/logout-all revoke the expected sessions.
7. Request password recovery for existing and nonexistent addresses and confirm the public response is indistinguishable; receive and consume a real reset link; prove prior sessions are revoked.
8. Verify disabled, unverified, reset-required and revoked-session access is denied.
9. Verify Data Centre video/notification routes accept a valid native session and reject revoked, expired, disabled and unverified sessions.
10. Verify private media still requires Backend viewer assertion and separate CDN origin authentication; never forward the user JWT as an origin service credential.
11. Separately verify staff sign-in, role checks, real second-factor challenge, step-up, logout and two-person role/onboarding gates before opening staff enrollment or identity-evidence intake.

## Production boundary

Source changes and CI results do not prove which auth build is running in production or whether mailbox delivery works. Do not disable historical production providers, alter account records, change routing or deploy solely based on this document. Require exact-head CI, real staging mailbox evidence, verified session lifecycle and separate explicit production-release approval.
