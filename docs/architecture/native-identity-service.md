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

## Delivery sequence

1. Repair and compile the current registration branch.
2. Add the native session/event/review schema and migration integration tests.
3. Implement session create/status/evidence-submit APIs and transition tests.
4. Implement authenticated reviewer APIs with two-person separation.
5. Wire Flutter registration to Testagram-owned session screens.
6. Remove Idswyft startup readiness, env vars, callback route and vendor-specific data.
7. Run migration, security, race, integration and full user-journey tests before enabling registration.
