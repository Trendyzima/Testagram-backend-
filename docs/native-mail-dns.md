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
  The deployment cross-checks the observed IP against two independent
  reflectors, requires it to equal the operator-confirmed repository variable
  `TESTAGRAM_NATIVE_MAIL_EXPECTED_EGRESS_IPV4`, and verifies the PTR.
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

The DNS token is separate from the Worker token. Use the existing
`CLOUDFLARE_DNS_API_TOKEN` GitHub secret scoped to Zone Read and DNS Edit for
`testagram.site` only.

## Cloudflare inbound bounce setup

One-time dashboard setup:

1. Open `testagram.site` in Cloudflare.
2. Go to Compute → Email Service → Email Routing → Settings → Subdomains.
3. Add `bounces.testagram.site`. Cloudflare provisions its MX, SPF and routing
   DKIM records. Do not enable Email Routing on the apex as part of this
   change and do not alter apex MX records.

Automated deployment:

- `scripts/deploy-cloudflare-bounce-worker.sh` probes the public Testagram API
  endpoint and requires HTTP 401 plus `X-Testagram-Bounce-Endpoint: native-v1`, proving the request reached the actual Go handler.
- It verifies `support_subaddress=true` through the Cloudflare Email Routing
  settings API. This setting is zone-wide; the deployment does not toggle it.
  Review existing apex/subdomain routes and enable it manually in Email Routing
  Settings before deployment.
- It verifies that the bounce subdomain publishes Cloudflare MX records.
- It temporarily deploys the Worker with `wrangler-probe.toml` on the account's
  existing `workers.dev` subdomain, without registering the email address.
- It reads the internally generated `TESTAGRAM_MAIL_BOUNCE_INGRESS_SECRET`
  from InstaBox and sends it directly to Cloudflare using `wrangler secret
  put`. The value is not printed, committed, or sent to the GitHub runner.
- It sends a signed health request to the temporary Worker. The Worker calls
  the public Testagram API and requires HTTP 401 plus
  `X-Testagram-Bounce-Endpoint: native-v1`; it returns 204 only if the API
  handler was reached.
- It deploys the final `wrangler.toml` with `workers_dev=false` and the literal
  `b@bounces.testagram.site` email rule, re-applies the HMAC secret, verifies
  that the rule targets this Worker, and confirms the temporary workers.dev
  endpoint is disabled. Only then does the deployment re-enable the outbox
  sender and restart the API; queued messages resume over Testagram's native
  SMTP transport.
- The Cloudflare account must already have a workers.dev subdomain configured
  under Workers & Pages. The deployment reads it but does not create or rename it.

Required GitHub configuration:

- Repository variable `TESTAGRAM_NATIVE_MAIL_EXPECTED_EGRESS_IPV4`: set only
  after InstaBox confirms the static dedicated egress IP and matching PTR.
- Secret `CLOUDFLARE_DNS_API_TOKEN`: Zone Read + DNS Edit on `testagram.site`.
- Secret `CLOUDFLARE_BOUNCE_WORKER_API_TOKEN`: Cloudflare control-plane access only, not an SMTP credential; Account Workers Scripts Read and Write;
  Zone Read; Zone Settings Read; and Email Routing Rules Read/Write on
  `testagram.site`.
- Secret `CLOUDFLARE_ACCOUNT_ID`: the Cloudflare account containing the zone
  and Worker.

Cloudflare's Email Routing subdomain must be enabled before the production
deployment. If its MX records are absent, deployment fails closed instead of
creating a Worker route that cannot receive mail.

## Bounce transport and integrity

Testagram sets each outbound envelope sender to
`b+<32-character-base64url-token>@bounces.testagram.site`. Cloudflare
subaddressing maps that recipient to the base Worker rule while preserving
the original recipient in the email event.

The Worker requires Cloudflare's prepended `Authentication-Results: mx.cloudflare.net;` header with `dmarc=pass` and relies on Cloudflare's inbound SPF/DKIM/DMARC validation; sender-supplied authentication-result headers are not trusted. It preserves the case-sensitive VERP local part, lowercases only the
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
- [ ] Observed egress matches the configured expected IPv4.
- [ ] PTR is `mail.testagram.site`; A record resolves to the same IPv4.
- [ ] SPF has exactly one record and authorizes InstaBox plus Cloudflare
      Email Routing.
- [ ] Testagram `tg2026` DKIM record resolves and verifies on a real message.
- [ ] Existing DMARC policy is preserved; Gmail and Outlook show SPF, DKIM
      and DMARC pass in recipient Authentication-Results.
- [ ] Cloudflare Worker route exists, plus-addressing works, and the shared
      HMAC secret is installed.
- [ ] Worker/API outage behavior is tested; no bounce is silently lost.
- [ ] Controlled messages reach actual Gmail and Outlook inboxes.
- [ ] A real hard-bounce DSN is correlated and suppresses once; replay creates
      no duplicate event or suppression.

CI and dry-run builds validate code and deployment wiring only. They do not
prove production SMTP reachability, DNS correctness, inbox placement or real
bounce delivery.