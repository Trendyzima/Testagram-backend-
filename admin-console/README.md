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

## First-owner bootstrap

After creating the first Appwrite account, verify its Appwrite user ID through
the Appwrite console, then grant the first owner role directly in PostgreSQL
using a protected operator session:

```sql
INSERT INTO testagram_auth.staff_roles(appwrite_user_id, role)
VALUES ('APPWRITE_USER_ID_FROM_ACCOUNT', 'owner');
```

Never accept the first owner ID from a public request or browser field. Record
the bootstrap action in the operator's deployment/change log.

## Deployment checks

- Keep the console on the private edge route and enforce exact-origin POSTs.
- Verify Appwrite email delivery and recovery redirects for
  `/reset-password`.
- Verify an unassigned Appwrite account receives 403 and cannot access the
  dashboard or invitation endpoint.
- Verify role changes are visible only after the server-side PostgreSQL update.
- Back up PostgreSQL and `ADMIN_SESSION_KEY`; do not back up browser tokens
  or place Appwrite API keys in client configuration.


## Granular permissions and sensitive-action controls

Apply `migrations/20261111_admin_security_controls.sql` after the staff-role
migration. PostgreSQL is the source of truth for role permissions and per-user
overrides. The owner is the only built-in principal with unrestricted access.
An owner can grant or explicitly deny a single permission to a staff account, change non-owner roles, and revoke staff access
through the protected API; a per-user override takes precedence over its role.

Initial role grants are deliberately narrow:
- administrator: identity review, moderation, advertising, marketplace, TV,
  engineering read, operations and audit read;
- engineer: engineering read and production deployment requests;
- operations: engineering read, operations and deployment requests;
- support: moderation;
- moderator: moderation.

Sensitive workflows are recorded in PostgreSQL and require two distinct
authorised approvers. The requester cannot approve their own request. Requests
expire after 24 hours. Password re-authentication creates a five-minute
step-up window in the encrypted staff session. Every request and approval is
written to the security audit ledger.

### Protected endpoints

- `POST /api/admin/step-up` — re-authenticate the current Appwrite account.
- `GET /api/admin/staff` — owner-only staff directory, including revoked access.
- `PUT /api/admin/staff/{id}/role` — owner-only role changes, with recent step-up.
- `DELETE /api/admin/staff/{id}` — owner-only staff access revocation; existing audit references are retained.
- `PUT /api/admin/staff/{id}/permissions` — owner-only, recent step-up required;
  grant or deny one permission with a reason.
- `POST /api/admin/sensitive-actions` — request one of
  `identity.approve`, `finance.execute`, `security.change` or
  `deployment.production`; requires the corresponding initiate permission
  and recent step-up.
- `GET /api/admin/sensitive-actions` — view the pending approval queue; requires
  `audit.read`.
- `POST /api/admin/sensitive-actions/{id}/decision` — approve or reject a pending request; requires the action-specific approval permission, a reason and recent step-up.
- `GET /api/admin/audit` — read the latest 200 security events; requires `audit.read`.

All state-changing HTTP methods require the exact configured Origin. Sensitive
request payloads are size-limited, canonicalised and SHA-256 fingerprinted.
The approval ledger records approvals and rejections but **does not execute the underlying
business operation**. Identity provisioning, payment execution, security
configuration changes and production deployment handlers must consume only
approved requests and independently revalidate the request hash, actor
permissions, target state and idempotency before performing the operation.

### Production rollout gates

This milestone provides password re-authentication and dual control. It does
not yet establish phishing-resistant MFA, hardware-key enforcement, or
business-service execution adapters. Do not enable production financial,
identity, security or deployment actions until those adapters are integrated,
MFA policy is enforced, the permission grant flow is reviewed, and end-to-end
tests are passing. The VPS operator must apply the new migration before
deploying this console revision.
