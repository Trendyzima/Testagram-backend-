# Testagram-native authentication acceptance

## Runtime boundary

The Flutter client authenticates directly with the Testagram Backend. The Backend owns password verification, canonical account state, short-lived access JWTs, PostgreSQL-backed sessions, rotating refresh credentials, and server-side revocation. Flutter stores only the refresh credential in platform secure storage; access JWTs remain in memory and are refreshed through the shared session manager.

Public signup at `/api/auth/signup` must remain fail-closed with HTTP `503 IDENTITY_REVIEW_REQUIRED` until identity evidence review and two-person approval are complete. The generic public route must not create an unverified identity or enqueue an OTP/verification email. Approved accounts are provisioned only through the application-scoped native identity provisioning path after evidence, consent, independent review, the two-person approval ledger and the one-time provisioning authorization have all been validated. Existing accounts may continue through their native login, email-verification and recovery lifecycle. `TESTAGRAM_IDENTITY_REVIEW_WEBAUTHN_ENABLED` controls identity application/review readiness; it must not provide a bypass for public signup. No external identity-provider runtime or fallback is permitted.

## Automated checks

The normal Backend CI exercises PostgreSQL identity schema and session lifecycle invariants, including refresh-token rotation, replay detection, logout, logout-all, and concurrent refresh attempts. Flutter CI exercises secure refresh-token persistence, in-memory access tokens, refresh retry after a 401, and sign-out behavior. API contract CI validates the published native-auth routes and security scheme.

## Deployed staging acceptance

The workflow `.github/workflows/native-auth-staging-smoke.yml` is manual and staging-only. It exercises the deployed API in this order:

1. Sign in with a dedicated, approved, email-verified staging identity.
2. Read `/api/me/profile` using the issued access JWT.
3. Rotate the refresh credential and confirm both tokens changed.
4. Replay the consumed refresh credential and confirm the entire session family is revoked.
5. Sign in again, call logout, and confirm the access JWT is rejected.
6. Sign in once more, call logout-all, and confirm the access JWT is rejected.

Before dispatching, configure these values in the GitHub `staging` environment:

- Variable `TESTAGRAM_STAGING_API_URL`: the HTTPS staging API origin, not the production API.
- Secret `TESTAGRAM_AUTH_SMOKE_EMAIL`: a dedicated staging test account that has completed approval and email verification.
- Secret `TESTAGRAM_AUTH_SMOKE_PASSWORD`: that staging account's password.

Dispatch only with `confirm_staging_only=true` after verifying the origin and account. The script refuses the production host `api.testagram.site`, does not print credentials or tokens, and intentionally invalidates its test sessions through replay detection and logout.

Do not use a production user or production credentials for this test.

## Staff authentication and identity-intake gate

Staff review routes require a native Backend session, an enabled staff principal, the appropriate role grant, an enrolled authenticator and a fresh signed MFA claim bound to the exact WebAuthn credential used for the assertion. Backend rechecks that credential's active, user-verified, non-cloned state for every privileged review request; revoking it invalidates existing elevation immediately. The Admin Console implements native WebAuthn registration/assertion with RP ID/origin validation, user verification, encrypted credential/challenge storage, single-use challenge consumption, counter/clone checks and a short-lived Backend-signed elevation token. Enrollment is independently authorized and gated by `TESTAGRAM_STAFF_WEBAUTHN_ENROLLMENT_ENABLED=true`.

The Backend checks PostgreSQL staff roles and active verified credentials, then calls the Admin Console's private `/internal/webauthn/readiness` endpoint on every identity-application request. The endpoint is protected by a shared `TESTAGRAM_WEBAUTHN_READINESS_TOKEN` (32+ random bytes) passed only between the API and Admin Console containers; the Admin Console independently verifies RP/origin configuration, credential encryption, database schema/privileges, backend health and at least four distinct eligible credentialed staff so the reviewer pair and approver pair can be disjoint. API intake also requires the explicit `TESTAGRAM_IDENTITY_REVIEW_WEBAUTHN_ENABLED=true` policy gate. Leave it false until real browser ceremony, recovery and replay acceptance pass. A non-null MFA-enrolment timestamp alone is never proof of a fresh assertion.

## Production boundary

A green CI run or staging smoke does not deploy schema changes, rotate production secrets, grant staff access, or change identity-review feature flags. While identity evidence review and two-person approval are incomplete, the live public-signup probe must return exactly `503 IDENTITY_REVIEW_REQUIRED`; this verifies the security gate and does not prove that public account creation is available. Do not create a production test account or send a production OTP while the gate remains active. Any later controlled account-creation acceptance requires independently verified evidence and approvals plus separate explicit release authorization.
