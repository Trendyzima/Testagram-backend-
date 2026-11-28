# Testagram-native authentication acceptance

## Runtime boundary

The Flutter client authenticates directly with the Testagram Backend. The Backend owns password verification, canonical account state, short-lived access JWTs, PostgreSQL-backed sessions, rotating refresh credentials, and server-side revocation. Flutter stores only the refresh credential in platform secure storage; access JWTs remain in memory and are refreshed through the shared session manager.

## Ordinary public account creation

Public self-service signup at `POST /api/auth/signup` is the standard account-creation path. It does not call identity-application intake, require identity evidence, wait for staff review, or require two-person approval. The native flow:

1. Validates email, username and password, applies rate limits, and stores an Argon2id password hash.
2. Creates a basic native identity in an unverified state and queues a six-digit OTP plus a single-use email-verification link in the same PostgreSQL transaction.
3. Requires successful OTP or link verification before sign-in. Verification activates the basic account and initializes its local profile.
4. Allows ordinary sign-in only after email verification, with account-disablement, password-reset state, session validity and token-version checks still enforced.

The mail outbox encrypts queued payloads with the stable `TESTAGRAM_MAIL_ENCRYPTION_KEY`. The deployment script generates and persists this key on the VPS when absent; the API and mail worker must receive the same value. `AUTH_PUBLIC_BASE_URL` must point to the canonical HTTPS web application so verification and password-reset links return to the Flutter app.

Identity evidence, human review and two-person approval are a separate optional assurance workflow. They do not grant ordinary accounts verified identity, staff status, administrator status or elevated permissions. Sensitive endpoints continue to enforce their own authorization and assurance requirements. The identity-review feature gate must remain disabled unless an operator explicitly enables it after its separate acceptance is complete. It must never be used as a public-signup switch.

## Automated checks

Backend CI covers native password validation, email verification rules, PostgreSQL identity/session lifecycle, refresh-token rotation, replay detection, logout, logout-all and concurrent refresh attempts. Flutter CI covers native auth client behavior, token-only email-verification callbacks, secure refresh-token persistence, in-memory access tokens, refresh and sign-out behavior. These automated checks do not substitute for a real mailbox and deployed staging acceptance.

## Identity-application readiness preflight

The manual identity-registration readiness workflow checks the separate evidence-review workflow. It may validate the explicit identity-review feature gate, PostgreSQL/WebAuthn quorum, private Admin Console attestation, private evidence storage and registration policy versions. It is not required to deploy ordinary native authentication or to create a basic Testagram account. Keep the identity-review gate disabled until real WebAuthn ceremony, recovery, replay and independent reviewer/approver acceptance pass.

## Deployed staging acceptance

The existing `.github/workflows/native-auth-staging-smoke.yml` is a staging-only session-lifecycle smoke test. It uses a dedicated, email-verified staging account to verify profile access, refresh rotation, replay-family revocation, logout and logout-all. It does not create a new account or prove mailbox delivery.

Before a release is accepted, run a separate real end-to-end signup acceptance against an isolated staging database and controlled mailbox:

1. Create a new ordinary account through the real Flutter signup page and deployed Backend.
2. Confirm the account is initially unverified and cannot sign in.
3. Confirm the actual verification email reaches the controlled mailbox; do not treat an outbox row as delivery.
4. Follow the real one-time link and, separately, test the six-digit OTP path.
5. Confirm verification activates the basic account and creates its local profile.
6. Confirm the same account can sign in through Flutter, load its profile, restore a session after app restart, rotate refresh credentials, and log out.
7. Confirm replay of a consumed verification token/OTP is rejected and that the consumed refresh credential revokes its session family.
8. Exercise password recovery with existing and nonexistent addresses, receive a real reset email, consume its single-use token, and confirm previous sessions are revoked.

Use only dedicated staging identities and mailboxes. Never use a production user's credentials or mailbox for acceptance.

## Staff authentication and identity assurance

Staff review routes require a native Backend session, an enabled staff principal, the appropriate role grant, an enrolled authenticator and a fresh signed MFA claim bound to the exact WebAuthn credential used for the assertion. Backend rechecks that credential's active, user-verified, non-cloned state for every privileged review request; revoking it invalidates existing elevation immediately. The Admin Console implements native WebAuthn registration/assertion with RP ID/origin validation, user verification, encrypted credential/challenge storage, single-use challenge consumption, counter/clone checks and a short-lived Backend-signed elevation token.

The Backend checks PostgreSQL staff roles and active verified credentials, then calls the Admin Console's private `/internal/webauthn/readiness` endpoint for identity-application intake when that separate feature is enabled. The endpoint is protected by a shared `TESTAGRAM_WEBAUTHN_READINESS_TOKEN` passed only between the API and Admin Console containers. Identity intake requires explicit operator opt-in and must remain closed until its independent acceptance passes. A non-null MFA-enrolment timestamp alone is never proof of a fresh assertion.

## Production boundary

A green CI run, a deployment or the existing session smoke does not prove that a real signup email was delivered. The production deployment workflow must not require identity-review WebAuthn readiness for ordinary native-auth deployment. Its safe signup readiness probe sends deliberately invalid signup data and expects `400 INVALID_PASSWORD`, proving the request reached ordinary validation without creating an account or queuing email. Real mailbox delivery, verification, sign-in and session restoration must still be proven in staging before production activation. Identity-evidence intake remains independently gated and must not be enabled merely to make ordinary signup work.
