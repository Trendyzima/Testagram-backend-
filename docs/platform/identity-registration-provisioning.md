# Identity registration and provisioning contract

## Status

The database migration `20261111_identity_registration_pipeline.sql` adds durable state for the registration workflow. It is **not** a public-signup activation and does not by itself implement application handlers, token issuance, Appwrite provisioning, email delivery, or canonical account linking.

Keep public registration disabled until every gate below is implemented and verified.

## Ownership

- Appwrite Auth owns credentials, sessions, password recovery and email verification.
- Backend owns application review, canonical Testagram identity, registration orchestration and consent evidence.
- PostgreSQL `testagram_local.accounts` owns the canonical account UUID and product profile.
- PostgreSQL `testagram_identity.appwrite_accounts` maps the Appwrite user ID to that canonical UUID.
- Data Centre and other services resolve the Appwrite session with Appwrite and require the canonical mapping. They must never create mappings during authentication.
- Flutter collects application data and consent, but is not an authority for identity approval or permissions.

## Required state machine

`pending → under_review → approved → provisioning → provisioned → verification_pending → verified`

Rejected, expired, cancelled and compensation-required states must be terminal or explicitly recoverable. No client request may set an application to approved, provisioned or verified.

## Application submission

The API must validate and normalize email, username and display name; enforce 18+ policy; require explicit Terms and Privacy acceptance; record distinct policy versions and server receipt timestamps; and rate-limit by IP and normalized email. A self-attested age checkbox is not identity verification. Do not persist identity-document images or raw identity numbers in this database. Store only an opaque evidence reference and a non-reversible fingerprint digest under a documented retention policy.

## Review and approval

Review actions require the existing `identity.review` permission, recent step-up authentication and the two-person sensitive-action policy. The requester cannot approve their own request. Both reviewer IDs, decisions, reasons, evidence audit references and timestamps must be retained. Rejection must not mint a token.

## Approval token

The issuer must run only after the approval transaction has two distinct authorized approvals. The signed token must include a cryptographically random JTI, application ID, subject, policy versions, approver references, issue time and expiry no longer than 15 minutes. Store SHA-256 hashes of the token and JTI, never the bearer token. Redemption must atomically mark the token consumed and bind it to one provisioning attempt. Replays must fail. A retry after an uncertain external Appwrite result must reconcile by the idempotency key, not mint a second account.

## Provisioning saga

1. Claim the application and approval token in a PostgreSQL transaction.
2. Create the Appwrite Auth user with email verification disabled.
3. Create `testagram_local.accounts` and `testagram_identity.appwrite_accounts` with one stable canonical UUID in a database transaction.
4. Record the Appwrite ID, canonical ID and provisioning state.
5. Dispatch an Appwrite verification email using the approved callback origin.
6. Mark verification pending; do not grant product access until Appwrite confirms verified email and all canonical mapping checks pass.

Appwrite and PostgreSQL do not share a distributed transaction. Use an explicit saga with idempotent steps, bounded retries, reconciliation and compensation. Never report success before the durable mapping and verification dispatch state are recorded. If account deletion compensation fails, quarantine the application and alert operations; do not silently retry account creation.

## Consent evidence

Persist Terms and Privacy separately, including exact published policy version, accepted timestamp and request ID. Record age confirmation separately from independently verified adult status. Policy version changes must not silently overwrite historical acceptance. The policy text and operator disclosures must be legally reviewed before public launch.

## Activation gates

- Reviewer queue and decision API are implemented and permission-tested.
- Token issuer and one-time redemption have concurrency/replay tests.
- Appwrite creation and canonical PostgreSQL mapping have failure-injection and reconciliation tests.
- Verification email delivery and callback origin allow-list are verified in production.
- Data Centre rejects unlinked and unverified identities.
- Terms/Privacy version and timestamp evidence is queryable by authorized auditors.
- Rate limits, abuse controls, monitoring, retention and deletion procedures are active.
- Web and Android registration journeys pass end-to-end tests.

Until all gates pass, the registration endpoint must remain unavailable to public clients.
