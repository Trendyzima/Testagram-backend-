# Testagram-native authentication (current mainline)

This document describes the current Backend and Flutter source implementation. It supersedes earlier independent-auth prototype notes that assigned user-session authority outside Testagram Backend.

## Authority and runtime boundary

- `cmd/api/auth.go`, `cmd/api/native_sessions.go` and `cmd/api/native_auth_boundary.go` implement the Testagram-native user credential and session boundary.
- PostgreSQL `testagram_auth.identities` owns canonical user UUIDs, password hashes, email verification, disabled/reset-required state and token version.
- PostgreSQL `testagram_auth.sessions` owns refresh-token hashes, session/family IDs, expiry, revocation and device fingerprints.
- Every protected Backend request validates the native access JWT and rechecks the current database session/account state. A valid signature alone is insufficient.
- External source IDs remain restricted historical reconciliation metadata only. The native API does not accept external identity-provider JWTs as a fallback.

## Credentials and session lifecycle

- New password hashes use Argon2id (64 MiB memory, 3 iterations, parallelism 2, 32-byte key).
- PBKDF2 verification remains only for explicitly migrated legacy hashes; successful login upgrades those hashes to Argon2id using a compare-and-swap update.
- Access JWTs are HS256, fixed issuer/audience, canonical user UUID, session UUID and token version, with a 15-minute lifetime.
- Refresh tokens are opaque, high-entropy, stored server-side only as one-way hashes, and expire after 30 days.
- Refresh is atomic and rotates the token. Reuse of an already-rotated token revokes the entire family.
- Logout revokes the current session. Logout-all increments token version and revokes every active session.
- Password recovery returns an anti-enumeration response, stores only a hash of a single-use 30-minute token, and a successful reset increments token version and revokes prior sessions.

## Login eligibility

A login succeeds only when the canonical identity exists, the password verifies, the account is not disabled, email is verified, password reset is not required, and any native registration-provisioning record is active. Pending or unapproved applications cannot create a login-capable account.

## Flutter client contract

The Flutter `NativeAuthClient` calls Backend native login/refresh/logout/recovery routes. Access tokens remain in memory; only the opaque refresh token is stored in platform secure storage. The app restores a session through refresh and validates the resulting access token against the Backend profile endpoint before showing the authenticated shell. Public Backend requests obtain the access token from this client and attach it as `Authorization: Bearer`.

A failed server-side logout is not treated as a successful logout: the client preserves the refresh credential and authenticated shell so the user can retry revocation when connectivity returns. A 401/403 from the logout endpoint means the server already considers the session invalid and local credentials can be cleared.

## Registration and verification

Ordinary public signup is the native email/password flow implemented by `authRespond` in `cmd/api/auth.go`. It creates `testagram_auth.identities` in an unverified state and queues a single-use OTP plus verification link in the same transaction. Email verification activates the account; login remains blocked until then. This ordinary path does not call identity-application intake or require identity evidence, staff WebAuthn, manual review or two-person approval. Its own fail-closed prerequisites are native auth configuration, PostgreSQL, rate limiting, password validation and transactional email delivery. The separate sensitive identity-evidence application remains a distinct Testagram-owned workflow: evidence, independent review, two-person approval and native provisioning apply only to that application path. `TESTAGRAM_IDENTITY_REVIEW_WEBAUTHN_ENABLED` is not a public-signup switch and must never gate `/api/auth/signup`, `/api/auth/resend-verification` or `/api/auth/verify-email`.

## External identity-provider retirement

The supported Backend runtime has no external identity-provider integration. Do not configure, call, import credentials from, or fall back to any hosted identity provider. Existing Testagram accounts authenticate only through canonical PostgreSQL identities and native password/session flows. Any historical provider identifier retained in an audit record is inert metadata and cannot establish identity or authorize account linking. Account recovery and identity reconciliation must use Testagram's own verified recovery and review procedures; never auto-link accounts by email equality alone.

## Staff console boundary

The Admin Console uses Backend-native login and an encrypted `testagram-staff` cookie. Staff requests check an enabled PostgreSQL principal and explicit role grants. Native WebAuthn registration/assertion is implemented in `admin-console/webauthn.go`: it requires an explicit RP ID/origin allowlist and user verification, stores credentials/challenges encrypted, consumes one-time challenges, checks authenticator counters/clone signals and issues a short-lived Backend-signed MFA elevation after a valid assertion. Enrollment requires independent authorization and `TESTAGRAM_STAFF_WEBAUTHN_ENROLLMENT_ENABLED=true`.

The identity-review API also requires a fresh signed MFA claim. Its service readiness flag `identityReviewWebAuthnReady` remains false in `cmd/api/main.go`, so public identity intake remains closed until the Admin Console-to-Backend elevation handoff and the full browser/recovery/replay path pass end-to-end staging acceptance. A stored `mfa_enrolled_at` timestamp alone is not proof of a fresh assertion.

## Deployment requirements and limits

Backend and user-aware Data Centre services must share the same `TESTAGRAM_AUTH_JWT_SECRET` (at least 32 random bytes). Database migrations, runtime grants, rate-limit storage, email transport and public callback URLs must be configured before staging acceptance.

Mainline source implementation is not proof of a production cutover. No production provider switch, public registration enablement, account activation or deployment is authorized by this document.
