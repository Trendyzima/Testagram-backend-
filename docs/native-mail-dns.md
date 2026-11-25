# Testagram native mail: InstaBox outbound, Cloudflare inbound

Testagram owns outbound mail delivery, DKIM signing, outbox queueing, retries,
VERP tokens, bounce correlation, suppression and delivery state. The Go mail
engine runs on InstaBox and connects directly to recipient MX servers. There
is no Mailjet, Resend or external outbound SMTP relay.

Cloudflare Email Routing is approved for one narrow purpose: receiving
messages sent to `bounces.testagram.site` and invoking the Testagram bounce
Worker. The Worker forwards the original MIME message to the Testagram HTTPS
API on InstaBox. It does not send application email.

## Network boundaries

- InstaBox's public HTTPS/raw-port gateway does not provide inbound SMTP/25.
  The deployment therefore disables the legacy SMTP bounce listener and
  does not publish host TCP/25 or 2525.
- Outbound direct-MX delivery requires TCP/25 egress. The deployment checks
  Gmail and Outlook MX connectivity from the actual InstaBox machine.
- A TCP connection alone is insufficient. InstaBox must confirm a stable,
  dedicated outbound IPv4 and configure its PTR to `mail.testagram.site`.
  The deployment discovers the observed IP from two independent reflectors,
  requires them to agree, and verifies the PTR. This cannot prove the address
  is static or dedicated; InstaBox provider confirmation remains mandatory.
- The Cloudflare DNS reconciler publishes `mail.testagram.site` as a
  DNS-only A record for that egress IP. It never treats an arbitrary
  IP-echo result as an inbound SMTP address.

If InstaBox cannot provide stable direct-SMTP egress and matching PTR, Gmail
and Outlook may reject or filter mail. Do not claim direct-MX production
acceptance until the provider confirms the network identity and live delivery
tests pass.

## Read-only network diagnostic

After the workflow update is merged, run `Deploy Testagram to InstaBox` manually with `diagnose_native_mail_egress=true` and `confirm_production_deploy=false`. This runs a read-only probe on the existing machine: two independent public IPv4 lookups, PTR, current `mail.testagram.site` A, and TCP/25 to Gmail and Outlook MX hosts. It sends no email and changes no DNS or services. Static/dedicated status still requires explicit InstaBox provider confirmation.

## Outbound DNS reconciliation

`scripts/reconcile-native-mail-dns.sh` is a legacy dedicated-SMTP-host tool; do not run it on InstaBox because it manages a separate host's bounce MX. Use only the InstaBox-specific reconciler below.

`scripts/reconcile-instabox-native-mail-dns.sh` runs only after the egress
identity and Testagram DKIM key are available. It:

- Creates/updates `mail.testagram.site` A as DNS-only.
- Merges `ip4:<INSTABOX_EGRESS_IPV4>` and
  `include:_spf.mx.cloudflare.net` into the single SPF TXT record on
  `bounces.testagram.site`, preserving existing mechanisms and the existing
  `all` qualifier.
- Publishes Testagram's `tg2026._domainkey.testagram.site` DKIM public key.
- Preserves an existing `_dmarc.testagram.site` policy; creates `p=none` only
  if no DMARC record exists.
- Never creates, replaces or changes MX records. Cloudflare Email Routing
  owns the inbound MX records on the bounce subdomain.

The reconciler receives the existing `CLOUDFLARE_API_TOKEN` from the
approved GitHub Actions inventory. It does not require a separate DNS secret.
The existing token must already have the Zone Read and DNS Edit permissions
needed for the records it manages; deployment fails closed if not.

## Cloudflare inbound bounce setup

One-time dashboard setup:

1. Open `testagram.site` in Cloudflare.
2. Go to Email → Email Routing → Settings → Subdomains.
3. Add `bounces.testagram.site`. Cloudflare provisions its MX, SPF and routing
   DKIM records. Do not enable Email Routing on the apex as part of this
   change and do not alter apex MX records.

Automated deployment:

Wrangler runs inside the official Node 22 Bookworm-slim Docker image on InstaBox, so the host does not need Node.js/npm installed. Wrangler is pinned to `4.113.0`; the npm cache is persisted locally.
- Before the API is started, InstaBox writes `TESTAGRAM_MAIL_DELIVERY_ENABLED=false` into its protected environment file. The outbox continues to queue messages but does not send them until the bounce Worker is ready.
- `scripts/deploy-cloudflare-bounce-worker.sh` probes the public Testagram API
  endpoint and requires HTTP 401 plus `X-Testagram-Bounce-Endpoint: native-v1`, proving the request reached the actual Go handler.
- It verifies `support_subaddress=true` through the Cloudflare Email Routing
  settings API. This setting is zone-wide; the deployment does not toggle it.
  Review existing apex/subdomain routes and enable it manually in Email Routing
  Settings before deployment.
- It verifies that the bounce subdomain publishes Cloudflare MX records.
- On first install, it bootstraps the Worker on the account's existing
  `workers.dev` subdomain without an email address, installs the HMAC secret,
  then deploys `wrangler-probe.toml` with the email rule. On later deployments,
  the probe config preserves the existing email rule while enabling the
  temporary HTTP health endpoint.
- The API derives a purpose-separated 256-bit bounce HMAC key from the
  existing `TESTAGRAM_MAIL_ENCRYPTION_KEY` using HKDF-SHA256 with fixed,
  versioned salt and info values. The deployment sends this derived key
  directly to Cloudflare using `wrangler secret put`; it does not generate
  or persist a second master credential.
- It sends a signed health request to the temporary Worker. The Worker calls
  the public Testagram API and requires HTTP 401 plus
  `X-Testagram-Bounce-Endpoint: native-v1`; it returns 204 only if the API
  handler was reached.
- It deploys the final `wrangler.toml` with `workers_dev=false`, `preview_urls=false`, and the literal
  `b@bounces.testagram.site` email rule, re-applies the HMAC secret, verifies
  that the rule targets this Worker, and confirms the temporary workers.dev
  endpoint and version preview URLs are disabled. Only then does the deployment re-enable the outbox
  sender and restart the API; queued messages resume over Testagram's native
  SMTP transport.
- The Cloudflare account must already have a workers.dev subdomain configured
  under Workers & Pages. The deployment reads it but does not create or rename it.

Use only the existing GitHub Actions credentials:

- `CLOUDFLARE_API_TOKEN` for Cloudflare zone/DNS/Email Routing API calls.
- `CLOUDFLARE_WORKERS_DEPLOY_TOKEN` for Wrangler Worker deployments.
- `CLOUDFLARE_ACCOUNT_ID` for the account containing the zone and Worker.
- `INSTABOX_API_KEY` for InstaBox machine access.
- `TESTAGRAM_MAIL_ENCRYPTION_KEY` for the encrypted outbox and HKDF-derived
  bounce HMAC key.

No additional GitHub secret or repository variable is required. The existing
Cloudflare tokens must already carry the necessary permissions; this design
does not create, rotate, or broaden credentials.

Cloudflare's Email Routing subdomain must be enabled before the production
deployment. If its MX records are absent, deployment fails closed instead of
creating a Worker route that cannot receive mail.

## Bounce transport and integrity

Testagram sets each outbound envelope sender to
`b+<32-character-base64url-token>@bounces.testagram.site`. Cloudflare
subaddressing maps that recipient to the base Worker rule while preserving
the original recipient in the email event.

The Worker requires Cloudflare's prepended `Authentication-Results: mx.cloudflare.net;` header with at least one Cloudflare-verified `spf=pass`, `dkim=pass`, or `dmarc=pass` and relies on Cloudflare's inbound SPF/DKIM/DMARC validation; sender-supplied authentication-result headers are not trusted. It preserves the case-sensitive VERP local part, lowercases only the
DNS domain, preserves the raw RFC 5322 bytes, and enforces a 1 MiB limit. It
signs timestamp, canonical recipient and SHA-256 body digest with HMAC-SHA-256
and posts the original MIME to `POST /internal/mail/bounce` over HTTPS.

The API rejects stale or invalid signatures, parses DSN/ARF, verifies the
token and original-recipient fingerprint against PostgreSQL, and applies the
existing idempotent event, delivery-state and suppression logic.

If the API returns a non-2xx response, the Worker throws rather than
acknowledging success. Cloudflare's SMTP retry behavior for Worker failures
must be verified with a controlled outage test; do not assume retry semantics
without evidence. Cloudflare also applies inbound authentication checks, so
real Gmail and Outlook DSNs must be tested through the full Email Routing
path.

## Production acceptance checklist

- [ ] InstaBox confirms stable, dedicated outbound IPv4 and configurable PTR.
- [ ] InstaBox outbound TCP/25 reaches Gmail and Outlook MX hosts.
- [ ] Two independent egress reflectors agree on the observed IPv4, and InstaBox confirms it is static/dedicated.
- [ ] PTR is `mail.testagram.site`; A record resolves to the same IPv4.
- [ ] SPF has exactly one record and authorizes InstaBox plus Cloudflare
      Email Routing.
- [ ] Testagram `tg2026` DKIM record resolves and verifies on a real message.
- [ ] Existing DMARC policy is preserved; Gmail and Outlook show SPF, DKIM
      and DMARC pass in recipient Authentication-Results.
- [ ] Cloudflare Worker route exists, plus-addressing works, and the
      HKDF-derived HMAC key is installed.
- [ ] Worker/API outage behavior is tested; no bounce is silently lost.
- [ ] Controlled messages reach actual Gmail and Outlook inboxes.
- [ ] A real hard-bounce DSN is correlated and suppresses once; replay creates
      no duplicate event or suppression.

CI and dry-run builds validate code and deployment wiring only. They do not
prove production SMTP reachability, DNS correctness, inbox placement or real
bounce delivery.