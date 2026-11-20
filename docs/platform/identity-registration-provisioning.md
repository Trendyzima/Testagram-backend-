# Native identity registration and provisioning

## Canonical owner

The Testagram Backend owns the application, verification session, private evidence metadata, reviewer decisions, two-person approval ledger, canonical account and native provisioning state. No Flutter client, Data Centre service, CDN, Federation service or Algorithm service can create a canonical user.

## Application journey

1. Flutter submits an idempotent application request with accepted age, Terms and Privacy declarations.
2. Backend creates an applicant-scoped verification session and returns only scoped capabilities.
3. Applicant reserves evidence uploads. Reservation retries with matching metadata converge on one evidence ID; conflicting metadata is rejected. Expired unuploaded reservations are retired before a new reservation is issued.
4. Backend verifies content type, file signature, byte count and SHA-256, then sends evidence through the dedicated Data Centre identity-evidence API. Data Centre independently verifies the B2 primary object, replicates and reads back the R2 copy, and reports `protected` before Backend accepts the evidence for review. Reviewer retrieval and retention deletion use the same owner-bound private API; Backend has no local object-store fallback for identity evidence.

Legacy compatibility note: the Backend's older `testagram_media.media_replication_outbox` worker and its identity-evidence bucket configuration are retained only for historical jobs/objects. New native registration and reviewer evidence operations do not call that worker. Do not treat the retired Backend-local smoke workflow as acceptance. Remove the legacy bucket configuration only after a production inventory confirms there are no pending identity-evidence jobs and any historical evidence has been migrated or intentionally retained.
5. Two distinct reviewers record matching identity decisions. Two distinct authorized approvers then approve account provisioning.
6. Backend consumes a one-time application authorization and creates the canonical identity/profile transactionally.
7. Backend sends a single-use verification email. The account remains unable to sign in until the verification token is accepted and the provisioning state becomes active.

## Native credential lifecycle

- Native password writes use Argon2id. Legacy PBKDF2 hashes are accepted only for controlled reconciliation and upgraded on successful login.
- A 15-minute access JWT and opaque 30-day refresh token are issued only to verified, active, non-disabled identities that do not require password reset.
- Refresh tokens are hashed in PostgreSQL and rotated atomically. Reuse revokes the token family.
- Logout revokes the current session; logout-all increments token version and revokes all active sessions.
- Password reset consumes a one-time hashed recovery token, changes the password, clears reset-required state and revokes all prior sessions.

## Fail-closed conditions

- Missing or unavailable PostgreSQL, policy versions, rate-limit storage, email transport, the dedicated Data Centre identity-evidence service, verified B2/R2 replication or approval records.
- A single reviewer or approver attempting to satisfy both independent approvals.
- Evidence integrity mismatch, missing replica, expired applicant capability or duplicate/conflicting reservation.
- Unverified email, disabled identity, pending provisioning or mandatory password reset.
- Staff reviewer lacking an enabled principal, explicit role grant or recorded MFA-enrollment marker.

## Staff WebAuthn and identity-intake gate

The Admin Console now contains native WebAuthn registration and assertion ceremonies. It requires an explicit RP ID/origin allowlist, user verification, encrypted credential and challenge storage, single-use challenge consumption, credential-counter/clone checks, rate limits and an independent enrollment authorization. A successful assertion produces a short-lived Backend-signed MFA elevation token.

The Backend identity-review handlers require a fresh signed MFA claim, an enabled native staff principal, the required role grant and an enrolled authenticator. The enrollment flow remains fail-closed unless its runtime readiness checks pass and `TESTAGRAM_STAFF_WEBAUTHN_ENROLLMENT_ENABLED=true`.

Important: the Backend now wires readiness to PostgreSQL credential state and requires an explicit `TESTAGRAM_IDENTITY_REVIEW_WEBAUTHN_ENABLED=true` policy gate at startup. It fails closed unless at least two distinct active, verified WebAuthn reviewers and two distinct active, verified WebAuthn owner/administrator approvers exist for the configured RP ID. The shared elevation signer is used by the Admin Console and has a PostgreSQL integration test proving that the Backend accepts its token and rejects it after native session revocation. Keep the policy gate unset until real browser ceremony, recovery and replay behavior pass end-to-end staging acceptance. Two-person approval does not substitute for MFA.

## Historical identity records

Historical external-provider mappings and audit rows are retained under restricted `testagram_identity.historical_external_*` tables. The offline reconciliation tool is `scripts/archive/legacy-identity-provider-import.sql`; it requires explicit source-account-to-canonical-UUID mappings, detects ambiguous collisions and does not import source password hashes. Reconciled users receive a reset-required credential state and must complete native password reset. Historical source IDs are not accepted as runtime credentials and cannot authorize native API routes.

## Staff controls

The Admin Console uses native Backend login and an encrypted `testagram-staff` cookie. Native WebAuthn registration/assertion is implemented in `admin-console/webauthn.go`; staff elevation is short-lived and Backend-signed. Staff enrollment still requires an independent authorization, enabled native staff principal, role grant, explicit RP/origin configuration and the enrollment feature gate. A stored MFA-enrollment timestamp alone is never accepted as proof of a fresh assertion.

## Release status

New identity application intake remains hard-disabled because `identityReviewWebAuthnReady` is still false in `cmd/api/main.go`. The Admin Console WebAuthn ceremony implementation exists, but the full review/elevation handoff and production readiness proof must pass end-to-end before that Backend gate is enabled. Setting `TESTAGRAM_IDENTITY_REGISTRATION_ENABLED=true` cannot bypass the code-level readiness gate.

Identity evidence uses the dedicated private Data Centre evidence client configured by `DATA_CENTRE_MEDIA_API_URL` and the distinct `IDENTITY_EVIDENCE_DATA_CENTRE_INTERNAL_TOKEN`; this identity-evidence path is not enabled by `FRESH_MEDIA_ENABLED`. It requires the Data Centre service to report ready and the submitted evidence to reach the protected, read-back-verified state. Registration also requires configured Terms/Privacy versions, real email delivery, reviewer retrieval/deletion and independent two-person approval. A merged PR, passing unit test or visible Create your account entry does not enable production registration, provision a real account or prove live email delivery.
