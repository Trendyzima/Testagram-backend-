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

## Staff MFA release blocker

The current Admin Console step-up handler reauthenticates the staff password; it does not verify a TOTP or passkey challenge. The Backend also checks the recorded `mfa_enrolled_at` marker and explicit role grants, but that marker is not proof that a second factor was just verified. Keep production staff identity-review activation fail-closed until real factor enrollment, challenge verification, recovery and replay protection are implemented and tested. Two-person approval does not substitute for MFA.

## Legacy Appwrite records

`scripts/migrate-appwrite-identities.sql` is a controlled reconciliation tool only. It uses explicit Appwrite-ID-to-canonical-UUID mappings, detects ambiguous collisions and does not import Appwrite password hashes. Reconciled users receive a reset-required credential state and must complete native password reset. It does not disable Appwrite or authorize Appwrite bearer tokens on native API routes.

## Staff controls

The Admin Console uses native Backend login and an encrypted `testagram-staff` cookie. It currently checks a recorded MFA-enrollment marker and password step-up but does not verify a TOTP/passkey challenge. Staff invitations and role-grant activation remain paused until dual-control onboarding and actual second-factor verification are implemented.

## Release status

The source implementation is present in mainline. The Backend API enforces `TESTAGRAM_IDENTITY_REGISTRATION_ENABLED=true` before accepting an application; the default is `false`, and an unset or empty value remains closed. Registration must remain disabled until the Data Centre media profile, separate identity-evidence service credential, live B2/R2 replication and restore, reviewer retrieval/deletion, real email delivery, genuine staff MFA and two-person approval gates pass. The deployment gate also requires both `DATA_CENTRE_MEDIA_ENABLED=true` and `FRESH_MEDIA_ENABLED=true`. The Flutter UI flag is separate and cannot override the Backend API gate. A merged PR, passing unit test or visible Create your account entry does not enable production registration, provision a real account or prove live email delivery.
