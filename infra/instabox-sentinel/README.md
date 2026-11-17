# Testagram InstaBox Sentinel

An external Cloudflare Worker that receives signed InstaBox lifecycle webhooks and requests recovery of the Testagram VPS immediately after an unexpected stop. It runs outside the VPS and complements (does not replace) the GitHub Actions watchdog.

## Safety contract

- Only `sandbox.stopped` is actionable. Suspended/idle, error, deleted, started and unrelated events are acknowledged but never force-woken.
- The Worker verifies the raw request body with InstaBox's HMAC-SHA256 signature, rejects timestamps outside five minutes, and checks delivery/event IDs.
- The event machine ID must match the unique InstaBox machine named `testagramdb`.
- Before starting, it fetches the current machine snapshot and requires `state=stopped`, `desiredState=running`, and no active `operationId`.
- Every start uses a deterministic `Idempotency-Key` based on the webhook delivery ID. A Durable Object serializes deliveries and stores event outcomes; failed provider calls are retried with an alarm.
- It never stops a machine, resumes an idle-suspended machine, changes storage, runs migrations, or touches database volumes.
- The public webhook responds quickly with HTTP 202; recovery runs asynchronously.
- No credential values are written to source, repository variables, logs, or test fixtures.

## Deploy

The GitHub Actions workflow `.github/workflows/deploy-instabox-sentinel.yml` deploys this directory when changes land on `main`. Configure these GitHub Actions secrets first:

- `CLOUDFLARE_API_TOKEN`: scoped to Workers Scripts read/edit and the required account permissions.
- `CLOUDFLARE_ACCOUNT_ID`: Cloudflare account ID.

In Cloudflare Workers & Pages, open `testagram-instabox-sentinel` → Settings → Variables and Secrets and add these as **Secret** values (not plaintext vars):

- `INSTABOX_API_KEY`: the same restricted InstaBox API key already used by the Testagram watchdog, with `machines:read` and `machines:write` on the workspace containing `testagramdb`.
- `INSTABOX_WEBHOOK_SECRET`: the signing secret beginning `whsec_` returned once when the InstaBox webhook endpoint is created.

Do not paste either credential into GitHub issues, chat, source code, or repository variables. Cloudflare cannot read GitHub Actions secrets; the same API key must be entered separately as a Cloudflare Worker secret. Do not create a new broad/full-access key just for this Worker.

## Register the InstaBox webhook

After the Worker is deployed, copy its HTTPS URL (Workers & Pages → the Worker → Visit). Create a webhook endpoint in the InstaBox dashboard's Webhooks page, or with the InstaBox CLI:

```sh
instabox webhooks create https://<your-worker-host>/webhooks/instabox --event sandbox.stopped
```

Save the displayed `whsec_...` value directly into the Cloudflare Worker secret `INSTABOX_WEBHOOK_SECRET`. InstaBox only displays the signing secret at creation; if it is lost, rotate the webhook secret and update the Cloudflare secret. The endpoint must subscribe to `sandbox.stopped`; do not subscribe to `sandbox.suspended` as a wake trigger.

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
