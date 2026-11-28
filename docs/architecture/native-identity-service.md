# Native Testagram identity service

## Purpose

The Backend is the sole authority for Testagram-native credentials, account status, session state and authorization. Ordinary public accounts use the native email/password signup and email-verification flow; government ID, selfie, identity evidence, manual review and identity approval are not prerequisites. The applicant/evidence/review lifecycle documented here is a separate optional assurance workflow for specifically protected capabilities. Flutter and private user-aware services consume Backend-issued native access JWTs; no external-provider bearer fallback is accepted by the native API path.

## Identity lifecycle

1. An applicant creates an idempotent identity application with accepted policy declarations.
2. Backend creates an applicant-scoped verification session. This is not an account or a login session.
3. Evidence upload reservations are private, size/type/SHA-256 bound and idempotent. Backend stores bytes privately and records only opaque object references and integrity metadata.
4. Evidence is sealed for manual review only after the dedicated Backblaze B2 object passes byte-count, content-type and SHA-256 verification and the audit requirements pass. Identity evidence is not replicated to InstaBox or Cloudflare R2.
5. Two distinct identity reviewers and two distinct account approvers must independently approve. One approval cannot provision an account.
6. A one-time application-scoped provisioning authorization is redeemed transactionally. Backend creates the canonical Testagram identity and profile and records the provisioning ledger.
7. Backend sends a single-use email-verification link. Only successful verification changes native provisioning to active.
8. The user can then set/use a native password and obtain a Testagram session.

## Native authentication

- Argon2id password hashes are the write format; PBKDF2 is accepted only to upgrade explicitly reconciled legacy hashes.
- Access JWT lifetime is 15 minutes. The opaque refresh token is stored as a one-way hash and rotates on every successful refresh.
- Session ID, token version, issuer, audience, canonical account enabled/disabled state and email verification are checked on protected requests. Ordinary authorization does not query the optional identity-application or provisioning ledger.
- Refresh-token replay revokes the token family. Logout revokes one session; logout-all revokes all sessions by incrementing token version.
- Password recovery is anti-enumerating and single-use; successful reset revokes all active sessions.
- Data Centre user-aware video/notification routes verify the same native JWT and independently query live PostgreSQL session/account state. Platform internal tokens remain separate.

## Staff review authority

The Admin Console authenticates through Backend native login and stores its session in an encrypted `testagram-staff` cookie. Staff authorization checks enabled PostgreSQL staff principals and role grants. Staff WebAuthn enrollment is independently authorized, challenges are one-time and persisted with expiry, registration and assertion responses are cryptographically verified by the backend, authenticator counters and replay conditions are enforced, and successful assertion produces backend-issued short-lived MFA elevation. Enrollment and production staff activation remain fail-closed until the configured RP ID/origins, dual-control onboarding and live PostgreSQL acceptance tests pass.

## External identity-provider exclusion

The supported product runtime has no external identity-provider integration, token exchange, SDK, hosted auth endpoint, database or fallback. Native Testagram identity records are authoritative. Historical external identifiers, if present in audit data, are inert metadata only; they cannot authenticate, provision, recover or link an account. Account recovery uses only Testagram's native verified recovery and review procedures. Email equality is never sufficient proof of account ownership.

## Release boundary

Optional identity-assurance application intake remains fail-closed until the dedicated private Backblaze B2 evidence bucket, B2-only upload/read-back/delete acceptance, email transport, policy versions, PostgreSQL grants and independent approval workflow pass staging acceptance. These requirements apply only to the separate identity-assurance workflow; they must never gate ordinary email/password signup, email verification, login, refresh or logout. Source-code implementation and green CI do not prove that production deployment or live mailbox acceptance has passed.
