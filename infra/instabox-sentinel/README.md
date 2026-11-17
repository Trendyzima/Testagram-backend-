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
- `INSTABOX_WEBHOOK_SECRET`: add this after creating the InstaBox webhook. The deployment workflow will transfer it into the Worker on the next deploy. Until then, the public webhook endpoint fails closed with HTTP 503.

The workflow uploads the existing `INSTABOX_WATCHDOG_API_KEY` as an encrypted Worker secret during deployment. Reuse that same restricted key; it must have `machines:read` and `machines:write` for the workspace containing `testagramdb`. Do not create a second provider key solely for this Worker. The webhook signing secret is separate: add the one-time `whsec_...` value as the GitHub Actions secret `INSTABOX_WEBHOOK_SECRET` after creating the endpoint, then re-run the deploy workflow so it is uploaded as an encrypted Worker secret. Never paste either value into chat, source code, issues, or repository variables.

## Register the InstaBox webhook

After the Worker is deployed, copy its HTTPS URL (Workers & Pages → the Worker → Visit). Create a webhook endpoint in the InstaBox dashboard's Webhooks page, or with the InstaBox CLI:

```sh
instabox webhooks create https://<your-worker-host>/webhooks/instabox --event sandbox.stopped
```

Save the displayed `whsec_...` value as the GitHub Actions secret `INSTABOX_WEBHOOK_SECRET`, then run the deploy workflow once more. InstaBox only displays the signing secret at creation; if it is lost, rotate the webhook secret and update the GitHub secret. The endpoint must subscribe to `sandbox.stopped`; do not subscribe to `sandbox.suspended` as a wake trigger.

Use the dashboard/CLI delivery test and confirm a valid test delivery receives HTTP 202. Then verify the Worker logs show only safe event/action metadata. Do not stop the production VPS to test recovery; use InstaBox's webhook test or a non-production machine first.

## Monitoring and rollback

- Keep the existing five-minute GitHub watchdog enabled as the fallback.
- Check InstaBox webhook delivery history and Cloudflare Worker logs after initial registration.
- A provider/API failure is retried by the Durable Object alarm; repeated failures remain visible in Worker logs and InstaBox delivery history.
- To disable event-driven recovery, disable/delete the InstaBox webhook endpoint. The GitHub watchdog remains independent.
- Health check: `GET /healthz`.

## Local tests

Run with Node.js 20 or newer:

```sh
node --test infra/instabox-sentinel/test/index.test.mjs
```
