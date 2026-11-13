# Testagram Native Identity Service

## Decision

Testagram owns registration verification sessions, state transitions, review queues, decision records and audit history. The Idswyft runtime dependency is removed from the target architecture: no Idswyft API key, webhook registration, webhook secret, callback endpoint or vendor-hosted verification URL is required.

## Assurance boundary

A Testagram session is not proof of a person's real-world identity by itself. The service distinguishes:
- **Self-declaration**: applicant-supplied claim; never sufficient to set a verified decision.
- **Document review**: a trained reviewer checks evidence held in restricted private storage; only an opaque reference and digest are stored in PostgreSQL.
- **Trusted external attestation**: optional adapter with a documented trust contract; not required for the core service.
- **Manual escalation**: ambiguous or suspicious evidence stays pending or is rejected; it is never auto-approved.

Age assurance and identity assurance are separate purposes and decisions. Passing an age threshold does not establish legal identity, and identity review does not bypass the two-person account-approval workflow.

## Session lifecycle

`created -> in_progress -> submitted -> manual_review -> verified|failed`

Sessions are application-scoped, expiring and single-use. Only SHA-256 token digests are stored. Every transition emits an append-only event in the same database transaction. Expired sessions cannot be revived; the applicant must obtain a new session. Duplicate submissions and decisions are idempotent.

## Evidence handling

Raw document images, selfies, biometric templates and full dates of birth must not be stored in the application database or logs. Evidence belongs in a private, encrypted object store behind short-lived authorization. Persist only an opaque evidence reference, content digest, evidence type, reviewer decision and retention metadata. Delete evidence when its documented retention period expires, preserving only the minimum audit record permitted by policy.

## Security controls

- Appwrite remains the sole user authentication provider.
- Applicant session tokens are high entropy, returned once, hashed at rest, and scoped to one application.
- Reviewer actions require authenticated staff authorization and are audited.
- A reviewer cannot approve their own application; two distinct authorized approvers remain mandatory for account activation.
- No public endpoint may set `verified`, write reviewer identity, or mint provisioning/approval tokens.
- No verification result is accepted from a client-supplied boolean, URL parameter or unsigned callback.
- Rate limits, request-size limits, no-store responses, idempotency and expiry are mandatory.
- Fail closed if the database, policy configuration or private evidence service is unavailable.

## Implemented backend surface

The native backend branch now provides:
- `POST /api/identity/applications`: creates an application and a Testagram-owned age-assurance session; returns application and session bearer tokens once.
- `GET /api/identity/applications/{application_id}`: application-scoped status lookup.
- `GET /api/identity/sessions/{session_id}`: session status using the session bearer token.
- `POST /api/identity/sessions/{session_id}/evidence/upload-url`: issues a short-lived PUT URL for an allowlisted document image or PDF in a private object-storage namespace.
- `POST /api/identity/sessions/{session_id}/evidence/submit`: verifies provider-observed size, content type and SHA-256 before moving the session into manual review.
- `GET /api/admin/identity/review-queue`: Appwrite-authenticated staff queue with short-lived private evidence URLs.
- `POST /api/admin/identity/sessions/{session_id}/decision`: requires two matching, distinct staff decisions; self-review is rejected.
- `POST /api/admin/identity/applications/{application_id}/approve`: connects the two verified reviewers to the existing two-person sensitive-action ledger and marks the application approved only after both approvals. It deliberately does not expose a provisioning bearer token to staff.
- `POST /api/identity/applications/{application_id}/provisioning-token`: the applicant proves possession of the application token and receives a 15-minute one-time provisioning token only after the two-person ledger is complete.
- `POST /api/identity/applications/{application_id}/redeem`: validates the one-time token and the caller's Appwrite JWT, requires an exact email match, links the Appwrite identity to a new canonical Testagram UUID, and creates the local account in a deactivated state until email verification. Provisioning is idempotent for the same Appwrite identity.

The runtime no longer registers an Idswyft webhook, checks Idswyft readiness, or calls an Idswyft API. The historical intake migration remains in migration history for upgrade compatibility. A follow-up migration removes its webhook-only column from the live schema; it is not a runtime dependency.

## Remaining delivery gates

1. Run and pass the isolated PostgreSQL migration/invariant workflow with the new evidence metadata migration.
2. Pass Go formatting, unit, race, vet, build, OpenAPI and container CI on this branch.
3. Add integration tests for the provisioning-token and redemption endpoints, including expired/replayed tokens, existing Appwrite identities, email mismatch, username conflict, and retry after partial Appwrite signup.
4. Complete Flutter integration tests for the approved-account provisioning step, secure token persistence/resume, and the staff review surface.
5. Exercise real private-bucket upload, exact Content-Length enforcement, checksum verification, short-lived reviewer download, expiry and retention deletion in staging.
6. Run complete applicant-to-provisioned-account end-to-end and security tests. Public registration remains disabled until these gates pass.
