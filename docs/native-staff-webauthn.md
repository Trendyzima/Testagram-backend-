# Native Testagram staff WebAuthn

This is a fail-closed staff MFA implementation. It binds WebAuthn credentials to
`testagram_auth.staff_principals.user_id` (the canonical Testagram staff UUID),
not to an email address, browser-local flag, or Console-generated identity.

## Required runtime configuration

Configure these values for the Admin Console process:

- `TESTAGRAM_WEBAUTHN_RPID`: the exact relying-party domain (hostname only).
- `TESTAGRAM_WEBAUTHN_ORIGINS`: comma-separated exact HTTPS origins served by the Console.
- `TESTAGRAM_AUTH_JWT_SECRET`: the existing native authentication signing secret (at least 32 characters).
- `ADMIN_SESSION_KEY`: the existing 32-byte Console session-encryption key.
- `TESTAGRAM_STAFF_WEBAUTHN_ENROLLMENT_ENABLED`: keep unset/false until the migration and all runtime prerequisites are verified; set true only during an approved enrollment window.

Do not use an IP address as the RP ID. Do not add wildcard origins. Local HTTP
origins are accepted only for localhost development.

## Private Backend-to-Console readiness handshake

The Backend and Admin Console must receive the same randomly generated
`TESTAGRAM_WEBAUTHN_READINESS_TOKEN` (at least 32 bytes). The Backend calls
`TESTAGRAM_ADMIN_CONSOLE_READINESS_URL`, which defaults to
`http://admin-console:8090/internal/webauthn/readiness` in the self-hosted
Compose network. The API and Console share the private `platform` network;
the readiness endpoint is not a public browser endpoint.

Do not point this URL at a public hostname or an untrusted service. The Backend
accepts only the exact private service hostname/path, sends the shared bearer
without following redirects, and fails closed if the attestation is missing,
invalid or not ready. Keep `TESTAGRAM_IDENTITY_REVIEW_WEBAUTHN_ENABLED=false`
until the API-side PostgreSQL quorum and the live Console attestation both pass. The Backend and Admin Console also calculate the same canonical fingerprint over the exact RP ID and allowed-origin set; a mismatch fails readiness closed.

## Migration

Apply `migrations/20261121_native_staff_webauthn.sql` using the existing
Testagram PostgreSQL migration process. The Console readiness endpoint checks
the required tables and runtime settings; it does not apply migrations itself.

## Controlled bootstrap

First enrollment is not open self-enrollment. An enabled staff principal must
have an unexpired row in `staff_webauthn_enrollment_authorizations`.

For the initial owner only, a deployment operator with database-owner access
may create a one-time bootstrap authorization after independently verifying
the canonical owner UUID and the production change approval:

```sql
INSERT INTO testagram_auth.staff_webauthn_enrollment_authorizations
  (user_id, authorization_method, reason, expires_at)
SELECT sp.user_id, 'operator_bootstrap',
       'Approved initial owner authenticator bootstrap',
       now() + interval '30 minutes'
FROM testagram_auth.staff_principals sp
JOIN testagram_auth.staff_role_grants rg
  ON rg.user_id = sp.user_id AND rg.revoked_at IS NULL
WHERE sp.user_id = '<canonical-owner-uuid>'::uuid
  AND sp.enabled = TRUE
  AND rg.role = 'owner';
```

The owner-only `POST /api/admin/staff/{id}/webauthn/enrollment-authorize`
endpoint is intentionally fail-closed and returns HTTP 501. A single owner
approval is not sufficient to authorize enrollment. It must remain closed
until the native two-person staff lifecycle transaction creates an
invitation-bound authorization after two distinct eligible approvers have
approved the exact target and role. The authorization must expire, be
single-use, and be consumed in the same PostgreSQL transaction that stores
the credential. Adding a second credential must require a separate,
target-bound authorization and fresh assertion from the target staff member.

## Identity-review readiness quorum

The Backend and Admin Console independently calculate the same PostgreSQL
readiness quorum. It requires at least two active, email-verified,
user-verified, non-cloned WebAuthn credential holders with the
`identity_reviewer` role **and no active `owner`/`administrator` role,
plus at least two different active credential holders with `owner` or
`administrator` and no active `identity_reviewer` role. A person holding
both sides is excluded from both quorum counts; four distinct credentialed
accounts alone are not enough. Revoked, cloned, unverified, disabled or
unverified-email credentials/accounts cannot satisfy readiness.

The Admin Console's authenticated internal readiness attestation must also
report this same disjoint quorum. Backend application intake remains closed
unless the explicit production policy gate is enabled and the live Admin
Console attestation and Backend PostgreSQL query both pass on each request.

## Staff principal lifecycle boundary

This implementation enrolls and authenticates already-provisioned staff principals.
It does not yet implement a complete staff invitation, role-change or revocation
workflow in the Admin Console; those handlers remain fail-closed and return
501. Do not enable general staff onboarding or use a one-person shortcut to
create staff roles. Until the dedicated two-person staff-provisioning workflow
is implemented and independently tested, initial principal creation and
emergency recovery must use the documented audited operator procedure with
separate approval evidence. Authenticator enrollment remains separately
authorized, target-bound, time-limited and single-use.

## Ceremony and elevation behavior

- Registration and assertion ceremony state is server-side AEAD-encrypted bytea, challenge-hash
  indexed, bound to user/ceremony by associated data, expires after at most two minutes,
  and is locked/consumed exactly once.
- The WebAuthn library validates client data, origin, RP ID hash, authenticator
  data, user presence/verification, attestation and assertion signatures.
- Credential JSON is AEAD-encrypted before it is written to PostgreSQL.
- Assertion completion updates the credential counter and consumes the
  challenge in one transaction. Clone warnings and counter races are denied.
- Successful assertions issue a five-minute HMAC-signed native Testagram access JWT with the existing issuer, audience, session ID and token version plus the backend-recognized `mfa_at` and exact WebAuthn credential identifier claims. The token is bound to the canonical subject, native session ID, token version and authenticator used for the verified assertion, stored only in the encrypted HttpOnly Console session, and forwarded server-to-server to the Backend identity-review API. The Backend checks that exact credential is still active, user-verified and not clone-warned on every privileged identity-review request, so credential revocation immediately invalidates outstanding elevation. The token is not exposed to browser JavaScript.
- Sensitive staff actions, permission changes, staff directory, audit and
  approval queue endpoints require this proof. Console-local MFA booleans are
  not accepted.

## Closing enrollment

After the approved enrollment window, set
`TESTAGRAM_STAFF_WEBAUTHN_ENROLLMENT_ENABLED=false` (or unset it), restart
the Console, and verify `GET /api/admin/webauthn/readiness` reports
`ready=false` while existing enrolled staff can still perform assertions.
Do not deploy with the enrollment gate open as a permanent public self-service
registration setting.
