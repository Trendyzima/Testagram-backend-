# Testagram Mail — Cloudflare Email Sending

Testagram uses Cloudflare Email Service for outbound transactional notifications. It does not host inboxes, accept inbound mail, or run an SMTP server on InstaBox.

## Architecture

- The Go API persists email work in `testagram_mail.outbox`.
- Signup verification and password-recovery token updates insert their encrypted outbox message in the same PostgreSQL transaction as the identity/token change, preventing a committed token with no corresponding queued email.
- Message bodies and one-time links are encrypted with AES-256-GCM before they enter PostgreSQL.
- A background worker claims rows with `FOR UPDATE SKIP LOCKED`, sends through Cloudflare Email Sending REST API, and records the Cloudflare message ID.
- Workers claim messages with a two-minute lease. An expired lease is quarantined as `uncertain` rather than reclaimed, because the provider may already have accepted the message. Definite retryable failures use exponential backoff and eventually enter the dead-letter state.
- Delivery is deliberately conservative rather than exactly-once: definite retryable failures are retried; ambiguous outcomes are quarantined for manual reconciliation to avoid blind duplicate sends. A provider acceptance is not proof of inbox delivery.
- If Cloudflare credentials are absent, the worker leaves queued mail pending rather than discarding it. Signup and resend therefore require operational monitoring of pending queue age; a queued message is not proof of provider acceptance.

The API and worker share the existing InstaBox process and PostgreSQL database. Cloudflare is the external delivery provider.

## Message classes and consent

- **Account verification:** native signup creates an unverified identity and queues a six-digit OTP plus a single-use verification link in the same PostgreSQL transaction. The OTP is HMAC-hashed in the identity row, expires after 10 minutes, and is limited to five guesses. The verification link expires after 24 hours. Successful verification creates the Testagram local profile; no staff approval is required.
- **Password recovery:** the native reset flow queues a single-use recovery link. It is transactional security mail and does not depend on social-email consent.
- **New follower / follow request:** sent only to an email-verified, active account that explicitly enabled the follower category.
- **New post from followed accounts:** sent only for public or follower-visible posts, to active, email-verified followers with the new-post category enabled. Blocked relationships are excluded.
- Social email preferences are independent and off by default. The first authenticated session asks the user whether they want to choose categories; declining records both categories as off. Users can change or withdraw either category in Email notifications settings. Transactional verification and recovery messages are never gated by these social preferences.

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
| `CLOUDFLARE_EMAIL_ACCOUNT_ID` | Runtime variable populated from the existing `CLOUDFLARE_ACCOUNT_ID` GitHub secret |
| `CLOUDFLARE_EMAIL_API_TOKEN` | Runtime variable populated from the existing `CLOUDFLARE_API_TOKEN` GitHub secret; token needs Email Sending: Edit |
| `AUTH_FROM_EMAIL` | `no-reply@testagram.site`, populated from the `TESTAGRAM_MAIL_FROM` GitHub variable |
| `TESTAGRAM_MAIL_ENCRYPTION_KEY` | 32 random bytes encoded as 64 hexadecimal characters |
| `TESTAGRAM_CLOUDFLARE_EVENTS_TOKEN` | High-entropy secret of at least 32 characters shared only with the Cloudflare Queue consumer Worker |

Generate the encryption key with:

```sh
openssl rand -hex 32
```

Configure these exact GitHub repository settings for the InstaBox deployment workflow:

- Existing repository secret: `CLOUDFLARE_ACCOUNT_ID`
- Existing repository secret: `CLOUDFLARE_API_TOKEN`
- New repository secret: `TESTAGRAM_MAIL_ENCRYPTION_KEY`
- New repository variable: `TESTAGRAM_MAIL_FROM` (defaults to `no-reply@testagram.site`)

The deployment workflow transfers these values to the existing InstaBox environment over the authenticated control-plane request. Never paste credentials into chat, source files, issue comments, or workflow logs.

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
- Social unsubscribe tokens are one-way hashed in PostgreSQL; never log or expose the raw token. GET requests only render confirmation; the explicit POST performs withdrawal.
- Social notification email is disabled by default and requires separate, per-category opt-in. The worker checks the current preference when claiming and again immediately before sending. Withdrawal cancels queued messages and clears their encrypted payloads. A message already in an external provider request cannot be recalled.
- Keep the encryption key stable across deployments. Losing it makes pending encrypted messages unreadable.
- Monitor dead-letter counts and provider acceptance; investigate ambiguous network failures because automatic retry can duplicate a message.
- Ordinary native account registration is self-service: email verification is required, but no administrator identity review or two-person approval is required to create a standard account. Keep separate staff/privileged-action approval controls intact; they must not gate ordinary signup.
- No production deployment or DNS change is part of the code pull request.


## Queue health checks

Run these read-only queries against the application PostgreSQL database during an operational review. They expose counts and age only; do not select encrypted payloads or recipient addresses for routine monitoring.

```sql
SELECT status, count(*) AS messages,
       min(created_at) AS oldest_created_at,
       max(attempts) AS highest_attempt_count
FROM testagram_mail.outbox
GROUP BY status
ORDER BY status;

SELECT count(*) AS pending_messages,
       extract(epoch FROM (now() - min(created_at)))::bigint AS oldest_pending_age_seconds
FROM testagram_mail.outbox
WHERE status='pending';

SELECT id, status, attempts, last_error_code, provider_message_id, updated_at
FROM testagram_mail.outbox
WHERE status IN ('dead','uncertain')
ORDER BY updated_at DESC
LIMIT 100;
```

Treat a growing pending age as a delivery-configuration or worker-capacity incident. Investigate dead rows by error code. Reconcile uncertain rows against Cloudflare Email Sending logs before any manual retry. Do not expose payload ciphertext, decrypted HTML, OTP values, password-reset links, or recipient addresses in tickets or logs.

The worker logs a single startup warning when provider configuration or the encryption key is missing. That warning does not imply queued mail was sent; messages remain pending until the worker is correctly configured.

## Ambiguous provider outcomes

A transport timeout, interrupted response, provider 5xx, malformed success response, missing provider message ID, or accepted response without a recipient outcome is not treated as a normal retryable failure. The outbox row moves to `uncertain`, retains its encrypted payload, and is excluded from automatic worker claims. Operators must reconcile the recipient and request against Cloudflare Email Sending logs before deciding whether to mark the row delivered or requeue it. Never blindly requeue an uncertain message: Cloudflare may already have accepted it.


Expired worker leases are also quarantined as `uncertain`, never automatically reclaimed. A process may have crashed after the provider accepted the request but before the database recorded the response. This conservative behavior can require manual review even when no request was actually sent; it deliberately prefers a delayed message over an undetected duplicate security email.



## Branded templates and one-click unsubscribe

All queued messages are rendered inside a responsive, table-based Testagram email shell with a plain-text alternative. The plain-text renderer preserves link destinations so recipients using text-only clients can still follow verification, recovery and social links. Set `AUTH_PUBLIC_BASE_URL`; the documented fallback is `https://testagram.site`.

Social activity messages include a visible category-specific unsubscribe link and the RFC 8058 headers `List-Unsubscribe` and `List-Unsubscribe-Post: List-Unsubscribe=One-Click`.

- `GET /api/email/unsubscribe?token=...` displays a confirmation page and does not change preferences. This prevents email security scanners that prefetch links from unsubscribing users.
- `POST /api/email/unsubscribe?token=...` consumes the token, withdraws only the matching social email category, and cancels that category's pending outbox rows in one PostgreSQL transaction.

Tokens contain 256 bits of random entropy, are stored only as SHA-256 hashes, expire after 365 days, and can be consumed once. Repeated POSTs are safe. Recipients can opt back in through authenticated Testagram email settings. A message already in the `sending` state or submitted to Cloudflare cannot be recalled; pending messages are cancelled and the worker rechecks consent before submitting.

The migration creates `testagram_mail.social_unsubscribe_tokens` and grants the existing application role only the required table and sequence permissions. No third-party preference center or new authentication provider is introduced.

## Delivery-event reconciliation

The backend accepts Cloudflare Email Sending lifecycle events at:

`POST /internal/mail/events`

The route is protected by a high-entropy `TESTAGRAM_CLOUDFLARE_EVENTS_TOKEN` bearer secret and validates the event source domain against the configured `AUTH_FROM_EMAIL` sender domain. It accepts the six Cloudflare Email Sending event types: delivered, deferred, bounced, failed, rejected and complained.

The migration `migrations/20261125_testagram_mail_delivery_events.sql` adds an idempotent event ledger, recipient fingerprints, delivery timestamps and a suppression registry. The event ID is unique. The message ID and keyed recipient fingerprint must both match the original outbox record before its delivery state can change. Events arriving before the outbox provider message ID is recorded remain unprocessed and are reconciled by the mail worker later.

Provider acceptance remains the outbox state `sent`; final provider lifecycle is stored separately in `delivery_status`. A delivered event means the recipient mail server accepted the message, not that a human opened it. Hard bounces and complaints create a recipient suppression. Soft bounces and sender-side rejections do not automatically suppress the recipient. Suppressed recipients are discarded before any later provider request.

### Cloudflare Queue wiring

Cloudflare's Email Sending event subscription is scoped to a sending domain. Create a Queue and subscribe it to the six Email Sending events for the verified `testagram.site` sending domain. Configure a Queue consumer Worker to forward each event to the backend endpoint above.

The backend and the Queue consumer must each receive the same independently generated high-entropy secret under the name `TESTAGRAM_CLOUDFLARE_EVENTS_TOKEN`. Configure it as a backend runtime secret and a Cloudflare Worker secret; never place it in source control or logs. The backend also needs the existing `AUTH_FROM_EMAIL` and stable mail encryption key.

A Queue consumer should acknowledge a message only after the backend returns HTTP 202. On timeout, network failure, or any non-202 response, retry the Queue message so Cloudflare can redeliver it. The backend event ledger makes those redeliveries idempotent.

Example consumer logic (adapt the origin and secret binding to the existing Cloudflare Worker deployment):

```ts
export default {
  async queue(batch, env) {
    for (const message of batch.messages) {
      const response = await fetch(
        `${env.TESTAGRAM_API_ORIGIN}/internal/mail/events`,
        {
          method: "POST",
          headers: {
            "Authorization": `Bearer ${env.TESTAGRAM_CLOUDFLARE_EVENTS_TOKEN}`,
            "Content-Type": "application/json",
          },
          body: JSON.stringify(message.body),
        },
      );
      if (response.status === 202) message.ack();
      else message.retry();
    }
  },
};
```

Do not acknowledge an event before the backend accepts it. Configure Queue retries and a dead-letter queue, then alert on messages reaching the dead-letter queue. The subscription and Queue consumer are Cloudflare account resources and must be provisioned and verified separately; adding the backend endpoint does not create them automatically.

### Event acceptance checks

1. Confirm all six event types arrive through the Queue consumer.
2. Confirm the same event delivered twice creates one ledger row and one state transition.
3. Confirm a delivered event updates the matching outbox row only when both message ID and recipient fingerprint match.
4. Confirm a hard bounce and a complaint suppress future sends to that recipient.
5. Confirm a soft bounce and sender-side rejection do not create recipient suppression.
6. Confirm an event that arrives before the provider message ID is persisted is reconciled later by the worker.
7. Confirm a wrong token or sending-domain mismatch is rejected.
8. Confirm the Queue message is retried for backend errors and acknowledged only after HTTP 202.

The backup-gated migration script and PostgreSQL Compose mount include the delivery-event migration. It must be applied before deploying the API code that reads the new columns and tables.
