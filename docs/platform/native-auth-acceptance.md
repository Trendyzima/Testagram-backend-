# Testagram-native authentication acceptance

## Runtime boundary

The Flutter client authenticates directly with the Testagram Backend. The Backend owns password verification, canonical account state, short-lived access JWTs, PostgreSQL-backed sessions, rotating refresh credentials, and server-side revocation. Flutter stores only the refresh credential in platform secure storage; access JWTs remain in memory and are refreshed through the shared session manager.

New account intake is separate from sign-in. Public registration remains fail-closed until the identity application, private evidence storage, manual review, independent two-person approval, provisioning, and email verification have completed. The legacy direct-signup endpoint remains disabled. No external identity-provider runtime or fallback is permitted.

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

## Staff authentication limitation

Staff review routes currently require a native Backend session, an enabled staff principal, a role grant, and a non-null MFA-enrolment timestamp. The Admin Console step-up currently reauthenticates the password; it does not perform a live TOTP or passkey challenge. The timestamp alone is not proof of a second factor. Therefore staff-review production activation remains gated until a real second-factor enrollment and challenge flow is implemented and verified end to end.

## Production boundary

A green CI run or staging smoke does not enable public registration, grant staff access, deploy schema changes, rotate production secrets, or change production feature flags. Production activation requires a separately approved change and a verified deployment/rollback plan.
