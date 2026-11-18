# Testagram Mail — Cloudflare Email Sending

Testagram uses Cloudflare Email Service for outbound transactional notifications. It does not host inboxes, accept inbound mail, or run an SMTP server on InstaBox.

## Architecture

- The Go API persists email work in `testagram_mail.outbox`.
- Message bodies and one-time links are encrypted with AES-256-GCM before they enter PostgreSQL.
- A background worker claims rows with `FOR UPDATE SKIP LOCKED`, sends through Cloudflare Email Sending REST API, and records the Cloudflare message ID.
- Expired worker leases are reclaimed. Failed submissions use exponential retry delays and eventually enter the dead-letter state.
- If Cloudflare credentials are absent, the worker leaves queued mail pending rather than discarding it.

The API and worker share the existing InstaBox process and PostgreSQL database. Cloudflare is the external delivery provider.

## Cloudflare account setup

1. Ensure `testagram.site` is active on Cloudflare authoritative DNS.
2. In Cloudflare Dashboard, open **Compute > Email Service > Email Sending**.
3. Select **Onboard Domain** and choose `testagram.site`.
4. Review and publish the records Cloudflare generates for the bounce subdomain, SPF, DKIM and DMARC. Do not overwrite existing SPF or DMARC records; merge policy values where required.
5. Wait until Cloudflare reports the sending domain as verified.
6. Create an account API token with **Email Sending: Edit** permission. Use an account-owned token with the narrowest available scope.
7. Confirm the account is entitled to Email Sending. Arbitrary-recipient sending currently requires a Workers Paid plan; verify the current Cloudflare plan and quota before production use.

Sender identity: `no-reply@testagram.site`.

No `mail.testagram.site` host, inbound MX record, mailbox, or Email Routing destination is required for outbound-only mail.

## Runtime configuration

Configure these values in the InstaBox deployment environment (never commit real values):

| Variable | Value |
|---|---|
| `CLOUDFLARE_EMAIL_ACCOUNT_ID` | Cloudflare account ID (32 hexadecimal characters) |
| `CLOUDFLARE_EMAIL_API_TOKEN` | Account API token with Email Sending: Edit |
| `AUTH_FROM_EMAIL` | `no-reply@testagram.site` |
| `TESTAGRAM_MAIL_ENCRYPTION_KEY` | 32 random bytes encoded as 64 hexadecimal characters |

Generate the encryption key with:

```sh
openssl rand -hex 32
```

Store the account ID as a repository variable if preferred; store the API token and encryption key as GitHub Actions secrets. Never paste either credential into chat, source files, issue comments, or workflow logs.

## Database migration

The migration is `migrations/20261122_testagram_mail_outbox.sql`. It is mounted into the existing PostgreSQL container as migration 057 and included in the backup-gated native identity migration script.

Before applying it to an existing VPS database:

1. Confirm a current, verified PostgreSQL backup exists.
2. Run the existing native identity migration workflow with its explicit confirmation input.
3. Verify `testagram_mail.outbox` exists and the `testagram_app` role has SELECT, INSERT and UPDATE.
4. Deploy the reviewed API image only after exact-head CI and PostgreSQL migration acceptance pass.

Do not run the migration manually against production without the backup and restore checks in `deploy/self-hosted/apply-native-identity-migrations.sh`.

## Delivery verification

After the domain is verified and the runtime configuration is installed:

1. Deploy the reviewed build to a non-production/staging environment first.
2. Trigger one controlled verification or password-reset notification to a mailbox you control.
3. Confirm the API accepted the request and a row moved from `pending` to `sent`.
4. Confirm a Cloudflare message ID was recorded.
5. Confirm the recipient received the message and Cloudflare Email Sending logs show the final delivery outcome.
6. Test a temporary provider failure and confirm the row is retried without losing its encrypted payload.
7. Test a stopped API process and confirm queued messages remain in PostgreSQL and resume after process recovery.

A successful REST API response means Cloudflare accepted or queued the recipient; it is not by itself proof of final inbox delivery. Use Cloudflare Email Sending logs for final delivery verification.

## Operational safety

- Do not log message bodies, tokens, API credentials, or recipient addresses.
- The database payload is encrypted at rest; the encryption key must be backed up separately from PostgreSQL.
- Keep the encryption key stable across deployments. Losing it makes pending encrypted messages unreadable.
- Keep registration enablement and the two-independent-reviewer approval gate unchanged.
- No production deployment or DNS change is part of the code pull request.
