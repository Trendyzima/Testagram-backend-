# Independent Testagram authentication

This document describes an optional legacy PostgreSQL password-auth flow. It is not the product's primary authentication path: Flutter signs in with Appwrite, and all user-data APIs validate Appwrite session JWTs and resolve the canonical account through `testagram_identity.appwrite_accounts`. These legacy routes are not enabled by default because `TESTAGRAM_AUTH_JWT_SECRET` is intentionally absent from the deployment templates. Do not point the Flutter client at these routes.

## Endpoints

All request bodies are JSON. Routes are under `/api/auth`:

- `POST /signup` — `{"email":"person@example.com","username":"my_name","password":"at least 12 characters"}`; sends a 24-hour email verification link.
- `POST /resend-verification` — `{"email":"person@example.com"}`; returns a generic response and safely rotates/resends a verification token for an unverified account.
- `POST /verify-email` — `{"token":"..." }`; verifies the address and initializes a local profile.
- `POST /login` — `{"email":"person@example.com","password":"..."}`; returns a 15-minute HS256 bearer access token.
- `POST /forgot-password` — `{"email":"person@example.com"}`; always returns a generic accepted response to reduce account enumeration.
- `POST /reset-password` — `{"token":"...","password":"new password with at least 12 characters"}`; consumes a single-use 30-minute token and increments the credential version.

Password hashes use PBKDF2-HMAC-SHA256 with a per-password random salt and 210,000 rounds. Verification and reset tokens are stored only as SHA-256 hashes. Login attempts and recovery requests are rate-limited in PostgreSQL. There is no Redis, Valkey, Upstash, or external cache dependency. Authentication responses are private/no-store.

## Required runtime configuration

Set these values through the deployment secret manager; never commit them:

- `DATABASE_URL` — Testagram-owned PostgreSQL database, with the migration below applied.
- `TESTAGRAM_AUTH_JWT_SECRET` — random secret of at least 32 characters; keep it stable across backend instances.
- `SMTP_HOST`, `SMTP_PORT` — SMTP server. Port 587 requires STARTTLS; port 465 uses implicit TLS.
- `SMTP_USERNAME`, `SMTP_PASSWORD` — authenticated SMTP credentials.
- `AUTH_FROM_EMAIL` — verified sender address.
- `AUTH_PUBLIC_BASE_URL` — HTTPS public frontend origin used to construct verification and reset links.

Mail delivery is deliberately fail-closed: SMTP TLS and authentication are required. If these values are missing or the mail server is unavailable, users cannot complete verification or receive recovery links. Configure and test the mail server before exposing these endpoints.

## Migration and release gates

1. Back up Testagram PostgreSQL and test a restore.
2. Apply `migrations/20261019_testagram_independent_auth.sql` after `migrations/20261019_testagram_local_accounts_posts.sql` and its replication prerequisites.
3. Configure the required secrets and test signup, verification, login, expired tokens, reset-token single-use, rate limits, duplicate email/username, and SMTP outage behavior in a staging deployment.
4. Point the client signup/login/recovery screens at these routes only after compatibility tests pass.
5. Keep `TESTAGRAM_LOCAL_PRODUCT_WRITES_ENABLED=false` until the client uses local identity consistently and existing-user migration/linking has been designed and verified.

## Existing users and boundaries

Historical accounts must be imported and linked through a trusted offline migration that verifies ownership and preserves the canonical Testagram UUID. Runtime identity linking against a legacy provider is not supported. Appwrite account IDs are mapped to canonical UUIDs only through the protected identity bridge; unknown mappings fail closed.

This legacy flow returns short-lived access tokens but does not implement refresh tokens, device/session management, MFA, passkeys, or a distributed revocation list. Its tokens are not accepted as Appwrite sessions by the product data APIs. The Backend does not proxy unknown capabilities to Supabase; unimplemented capabilities return an explicit not-implemented response.
