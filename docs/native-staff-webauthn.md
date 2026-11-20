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

After an owner has enrolled, further staff enrollment authorizations are
created by an enrolled owner through
`POST /api/admin/staff/{id}/webauthn/enrollment-authorize`. That endpoint
requires a fresh WebAuthn elevation and refuses self-authorization. Each grant expires after 30 minutes and is consumed in the same PostgreSQL transaction
that stores a new credential. Adding a second credential also requires a fresh assertion
from the target staff member and a separate independent authorization.

## Ceremony and elevation behavior

- Registration and assertion ceremony state is server-side AEAD-encrypted bytea, challenge-hash
  indexed, bound to user/ceremony by associated data, expires after at most two minutes,
  and is locked/consumed exactly once.
- The WebAuthn library validates client data, origin, RP ID hash, authenticator
  data, user presence/verification, attestation and assertion signatures.
- Credential JSON is AEAD-encrypted before it is written to PostgreSQL.
- Assertion completion updates the credential counter and consumes the
  challenge in one transaction. Clone warnings and counter races are denied.
- Successful assertions issue a five-minute HMAC-signed native Testagram access JWT with the existing issuer, audience, session ID and token version plus the backend-recognized mfa_at claim. The token is bound to the canonical subject and encrypted HttpOnly Console session, then forwarded to the Backend identity-review API.
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
