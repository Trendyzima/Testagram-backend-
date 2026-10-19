# Independent Testagram authentication

This is an additive, opt-in rollout for **new Testagram-native identities**. It does not turn Supabase off globally and does not migrate existing Supabase passwords. The backend endpoints use the Testagram PostgreSQL database directly and do not call Supabase Auth, Cloudflare APIs, Redis, or Upstash during signup, login, verification, or password recovery.

## Endpoints

All request bodies are JSON. Routes are under `/api/auth`:

- `POST /signup` — `{"email":"person@example.com","username":"my_name","password":"at least 12 characters"}`; sends a 24-hour email verification link.
- `POST /verify-email` — `{"token":"..." }`; verifies the address and initializes a local profile.
- `POST /login` — `{"email":"person@example.com","password":"..."}`; returns a 15-minute HS256 bearer access token.
- `POST /forgot-password` — `{"email":"person@example.com"}`; always returns a generic accepted response to reduce account enumeration.
- `POST /reset-password` — `{"token":"...","password":"new password with at least 12 characters"}`; consumes a single-use 30-minute token and increments the credential version.

Password hashes use PBKDF2-HMAC-SHA256 with a per-password random salt and 210,000 rounds. Verification and reset tokens are stored only as SHA-256 hashes. Login attempts and recovery requests are rate-limited in PostgreSQL, so Redis/Upstash availability is not required. Authentication responses are private/no-store.

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

Supabase password hashes are not readable through normal application APIs. Existing users therefore cannot simply be copied into this credential table. They need a secure account-linking/verified reset migration, including a collision policy for UUIDs, usernames, and emails. Until that exists, this service only authenticates identities created in Testagram's local auth store.

This first release returns short-lived access tokens but does not implement refresh tokens, device/session management, MFA, passkeys, or a distributed revocation list. Password reset increments a token version in the database, but existing access tokens expire naturally within 15 minutes; do not describe this as immediate global session revocation. The gateway still has other legacy Supabase-backed capabilities, and a Cloudflare-only public route remains a separate availability dependency.
