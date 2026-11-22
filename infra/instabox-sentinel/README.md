# Testagram InstaBox Sentinel

An external Cloudflare Worker that receives signed InstaBox lifecycle webhooks and requests recovery of the Testagram VPS immediately after an unexpected stop. It runs outside the VPS and complements (does not replace) the GitHub Actions watchdog.

## Safety contract

- Only `sandbox.stopped` is actionable. Suspended/idle, error, deleted, started and unrelated events are acknowledged but never force-woken.
- The Worker verifies the raw request body with InstaBox's HMAC-SHA256 signature, rejects timestamps outside five minutes, and checks delivery/event IDs.
- The event machine ID must match the unique InstaBox machine named `testagramdb`.
- Before starting, it fetches the current machine snapshot and requires `state=stopped`, `desiredState=running`, and no active `operationId`.
- Every start uses a deterministic `Idempotency-Key` based on the webhook delivery ID. A Durable Object serializes deliveries and stores event outcomes; failed provider calls are retried with exponential backoff for up to eight attempts, then marked failed and logged without exposing credentials.
- It never stops a machine, resumes an idle-suspended machine, changes storage, runs migrations, or touches database volumes.
- The public webhook responds quickly with HTTP 202; recovery runs asynchronously.
- No credential values are written to source, repository variables, logs, or test fixtures.

## Deploy

The GitHub Actions workflow `.github/workflows/deploy-instabox-sentinel.yml` deploys this directory when changes land on `main`. Configure these GitHub Actions secrets first:

- `CLOUDFLARE_API_TOKEN`: scoped to Workers Scripts read/edit and the required account permissions.
- `CLOUDFLARE_ACCOUNT_ID`: Cloudflare account ID.
- `INSTABOX_WATCHDOG_API_KEY`: the existing Testagram secret already used by the five-minute watchdog. The deploy workflow transfers its value directly into the Worker as the encrypted `INSTABOX_API_KEY` secret; it is never printed or committed.
- The webhook signing secret is provisioned directly from InstaBox into the Cloudflare Worker during the one-time webhook bootstrap. It is not printed or committed. The old GitHub Actions `INSTABOX_WEBHOOK_SECRET` entry is not used by subsequent deployments.

The workflow uploads the existing `INSTABOX_WATCHDOG_API_KEY` as an encrypted Worker secret during deployment. Reuse that same restricted key; it must have `machines:read` and `machines:write` for the workspace containing `testagramdb`. Do not create a second provider key solely for this Worker.

The one-time bootstrap registers or finds the exact endpoint `https://testagram.site/webhooks/instabox`, enforces the single `sandbox.stopped` subscription, rotates the signing secret, uploads the fresh value directly to Cloudflare, and sends a test delivery. Subsequent deployments preserve the Worker signing secret and do not rotate it. The signing secret is never printed or committed. Never paste credentials into chat, source code, issues, or repository variables.

## Register the InstaBox webhook

The production endpoint is `https://testagram.site/webhooks/instabox`. The one-time bootstrap is performed by a push whose commit message begins with `ops(sentinel)`. It finds an existing endpoint at that exact URL or creates one, restricts it to `sandbox.stopped`, rotates the signing secret to invalidate any previously exposed value, stores the new secret directly in Cloudflare, and verifies a `webhook.test` delivery. Do not subscribe to `sandbox.suspended` as a wake trigger.

Use the dashboard/CLI delivery test and confirm a valid test delivery receives HTTP 202. Then verify the Worker logs show only safe event/action metadata. Do not stop the production VPS to test recovery; use InstaBox's webhook test or a non-production machine first.

## Monitoring and rollback

- Keep the existing five-minute GitHub watchdog enabled as the fallback.
- Check InstaBox webhook delivery history and Cloudflare Worker logs after initial registration.
- A provider/API failure is retried by the Durable Object alarm; repeated failures remain visible in Worker logs and InstaBox delivery history.
- A start request is not recorded as recovered when it is merely accepted: the Durable Object polls the exact InstaBox operation and then independently reads the exact machine until `state=running` and `desiredState=running` are confirmed. Pending operations remain `starting`; operation failures, identity mismatches and a ten-minute verification timeout fail closed and remain visible in logs.
- To disable event-driven recovery, disable/delete the InstaBox webhook endpoint. The GitHub watchdog remains independent.
- Health check: `GET /healthz`.

## Local tests

Run with Node.js 20 or newer:

```sh
node --test infra/instabox-sentinel/test/index.test.mjs
```
