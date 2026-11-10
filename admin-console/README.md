# Testagram Staff Console

The private staff console uses Appwrite for authentication and PostgreSQL for
server-controlled staff roles. It has no Supabase dependency.

## Required configuration

- `APPWRITE_ENDPOINT` and `APPWRITE_PROJECT_ID` identify the Appwrite project.
- `APPWRITE_API_KEY` is required only for owner-issued staff invitations. Give
  it only the Appwrite user-management scope required to create/delete users.
  Keep it server-side; never expose it to the browser.
- `DATABASE_URL` points to the canonical Testagram PostgreSQL database with
  `migrations/20261110_staff_roles.sql` applied.
- `ADMIN_SESSION_KEY` is 64 hex characters generated from 32 random bytes.
- `ADMIN_PUBLIC_URL` must be the exact HTTPS origin routed to this service.

## Authentication and authorization

1. Staff sign in through Appwrite email/password sessions.
2. The console encrypts the Appwrite session secret in an HttpOnly, Secure,
   SameSite=Strict cookie.
3. Each request validates the session against Appwrite's Account endpoint.
4. The server reads the staff role from `testagram_auth.staff_roles`. Roles
   are never accepted from browser fields, Appwrite user metadata, or JWT
   claims supplied by the client.
5. Staff accounts are invitation-only. An owner creates an Appwrite user,
   assigns the PostgreSQL role and triggers Appwrite password recovery.
6. Role assignment changes are recorded in `testagram_auth.staff_role_audit`.

Supported roles: owner, administrator, engineer, operations, support and
moderator. Only owners can invite staff.

## Deployment checks

- Keep the console on the private edge route and enforce exact-origin POSTs.
- Verify Appwrite email delivery and recovery redirects for
  `/reset-password`.
- Verify an unassigned Appwrite account receives 403 and cannot access the
  dashboard or invitation endpoint.
- Verify role changes are visible only after the server-side PostgreSQL update.
- Back up PostgreSQL and `ADMIN_SESSION_KEY`; do not back up browser tokens
  or place Appwrite API keys in client configuration.
