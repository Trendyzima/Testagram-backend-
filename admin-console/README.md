# Testagram staff console

This service adds a browser-accessible operations workspace at `https://ops.testagram.site` when deployed through the self-hosted Compose stack. It is a separate Go service; it does not expose the private platform, Prometheus, or control-plane ports directly.

## Features in this first release

- Sign-in, account request/sign-up, sign-out, password recovery and password update.
- Server-encrypted, HttpOnly, SameSite=Strict session cookie; browser JavaScript never receives access/refresh tokens.
- Server-side role verification from Supabase Auth `app_metadata.staff_role`. Roles in user-editable `user_metadata` are ignored.
- Owner-only staff invitations. Invite roles: `administrator`, `engineer`, `operations`, `support`, and `moderator`. The `owner` role cannot be granted by the invite form.
- Operational overview using the internal Prometheus API and the API readiness endpoint, including API/platform availability, request rate, 5xx rate, p95 latency, database-pool use, and pending queue depth when metrics are available.
- Dashboard metrics refresh every 30 seconds while the approved staff dashboard is visible; polling pauses in hidden tabs, and a manual refresh remains available.
- Missing Prometheus samples are shown as unavailable instead of being incorrectly presented as a real zero. The Grafana target-up summary counts the five configured scrape jobs.
- Links to the backend repository and private Grafana dashboard.

## Configure before deploying

1. Set `SUPABASE_URL` to the Supabase API/Auth base URL and `SUPABASE_PUBLISHABLE_KEY` to its publishable/anon key.
2. Generate a unique encryption key:
   ```bash
   openssl rand -hex 32
   ```
   Put the 64-character result in `ADMIN_SESSION_KEY`. Never commit it or reuse the Supabase secret key.
3. Set `ADMIN_DOMAIN=ops.testagram.site` and `ADMIN_PUBLIC_URL=https://ops.testagram.site`. Point DNS for that hostname to the VPS and allow inbound TCP 80/443 for Caddy TLS.
4. Add `https://ops.testagram.site/` and `https://ops.testagram.site/reset-password` to Supabase Auth's allowed redirect URLs. Confirm the recovery email template returns the access token in the URL fragment for the recovery flow.
5. To use the owner-only invite form, set `SUPABASE_SERVICE_ROLE_KEY` to the server-side secret/service-role key. It is passed only to the private console container and is never returned to the browser. Leave it blank if invitations will be managed in the Supabase Auth dashboard.
6. Protect `deploy/self-hosted/.env` with `chmod 600` and use distinct random secrets for each service.

Start or update the stack from the repository root:

```bash
docker compose --env-file deploy/self-hosted/.env -f deploy/self-hosted/docker-compose.yml config --quiet
docker compose --env-file deploy/self-hosted/.env -f deploy/self-hosted/docker-compose.yml up -d --build admin-console caddy
docker compose --env-file deploy/self-hosted/.env -f deploy/self-hosted/docker-compose.yml ps admin-console caddy
```

Open `https://ops.testagram.site`. The console exposes only port 443 through Caddy; its service port, Prometheus and the platform service remain internal.

## First owner and staff provisioning

Do not make the first user an owner through a public signup form. Create/verify the owner's account in the trusted Supabase Auth admin interface, then set its server-controlled Auth `app_metadata.staff_role` to `owner`. For example, with the Supabase Admin SDK in a trusted server environment, set `app_metadata: { staff_role: "owner" }` for the owner's user ID. Never set this in `user_metadata`, browser code, a public SQL policy, or a user-submitted signup payload.

The owner can then sign in and invite staff from the dashboard. New public sign-ups never receive a role automatically. A signed-in user without an approved role sees an approval-pending screen and cannot read monitoring data or invite staff. Role changes are enforced by the console server on every protected request, not just hidden UI elements.

## Roles and boundaries

- `owner`: dashboard, invite staff.
- `administrator`, `engineer`, `operations`, `support`, `moderator`: dashboard and monitoring overview.
- No staff role: no access to protected dashboard APIs.
- The owner invite form cannot assign `owner`; owner provisioning is a separate trusted operation.

The current dashboard is observability-first. It does not yet provide destructive infrastructure controls, user moderation actions, database console access, deploy/rollback actions, or a complete durable staff audit trail. Those should be separate, audited, least-privilege workflows with explicit confirmation and tests before being added.

## Validation and remaining release gates

CI builds the non-root container and tests session encryption, cookie tampering, origin checks and role derivation. Before production use, test Supabase email delivery and redirect allowlists, invite delivery, password recovery, session refresh/revocation, owner provisioning, the dashboard against a running Prometheus stack, and an end-to-end unauthorized-user denial. Configure backups and MFA in Supabase Auth; prefer MFA for owner/admin accounts. This console does not itself prove that email delivery or MFA has been enabled.
