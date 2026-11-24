# Testagram inbound bounce Worker

This Worker is an **inbound-only** adapter. It receives DSNs sent to Testagram
VERP return paths and posts the original RFC 5322 message to Testagram on
InstaBox over HTTPS. It does not send application mail and is not an SMTP relay.

## One-time Cloudflare setup

1. In Cloudflare, open `testagram.site` → Email → Email Routing → Settings →
   Subdomains. Add `bounces.testagram.site`. Cloudflare provisions the
   subdomain's inbound MX and routing authentication records. Do not change
   the existing `testagram.site` apex MX records.
2. Review all existing Email Routing rules, then enable subaddressing in
   Email Routing → Settings. This is a **zone-wide** setting: plus-address
   matching changes for every apex and subdomain rule, not only Testagram's
   bounce address. The deployment checks that it is already enabled and
   deliberately does not toggle it.
3. Create a dedicated Cloudflare API token and save it as the GitHub Actions
   production secret `CLOUDFLARE_BOUNCE_WORKER_API_TOKEN`. This is
   Cloudflare control-plane access only, not an SMTP or outbound mail
   credential. Required scopes:
   - Account: Workers Scripts Read and Write
   - Zone `testagram.site`: Zone Read
   - Zone `testagram.site`: Zone Settings Read
   - Zone `testagram.site`: Email Routing Rules Read and Write
4. Keep `CLOUDFLARE_DNS_API_TOKEN` scoped to Zone Read and DNS Edit on
   `testagram.site`. It is used only for A/SPF/DKIM/DMARC DNS records.
5. Ensure `CLOUDFLARE_ACCOUNT_ID` identifies the account containing the zone
   and Worker.
6. After InstaBox confirms its stable, dedicated SMTP egress IPv4 and
   provider-configured PTR, set repository variable
   `TESTAGRAM_NATIVE_MAIL_EXPECTED_EGRESS_IPV4` to that exact IPv4.

## Read-only InstaBox network diagnostic

After the workflow change is on the default branch, run `Deploy Testagram to
InstaBox` manually with `diagnose_native_mail_egress=true` and
`confirm_production_deploy=false`. It reports two IPv4 observations, reverse
PTR, the current `mail.testagram.site` A record, and TCP/25 connectivity to
Gmail and Outlook MX hosts. It does not send email, change DNS, deploy
containers, or issue machine start/stop commands; the machine must already
be running. A single probe cannot prove that an IP is static or dedicated;
that still requires explicit InstaBox confirmation.

## Automated deployment sequence

Wrangler is executed inside the official Node 22 Bookworm-slim Docker image on InstaBox; the host does not need Node.js or npm installed. Wrangler itself is pinned to version `4.113.0` and its npm cache is persisted locally.

1. InstaBox pauses the native outbox sender before starting the new API, so
   queued messages cannot leave before bounce ingress is ready. It verifies
   outbound TCP/25 to Gmail and Outlook, cross-checks the egress IPv4 against
   two independent reflectors, and verifies PTR is `mail.testagram.site`.
2. The InstaBox DNS reconciler creates/updates DNS-only `mail.testagram.site`
   A, merges the InstaBox IPv4 and Cloudflare Email Routing include into the
   single SPF record on `bounces.testagram.site`, publishes Testagram's
   `tg2026` DKIM selector, and preserves DMARC. It never changes MX records.
3. The deployment probes `POST https://api.testagram.site/internal/mail/bounce`
   and requires HTTP 401 plus `X-Testagram-Bounce-Endpoint: native-v1`,
   proving the public route reaches the actual native handler.
4. The Cloudflare deployment script verifies Email Routing is enabled,
   `support_subaddress=true`, all bounce-subdomain MX targets belong to
   Cloudflare, and no existing routing rule would be taken over.
5. On first install, it bootstraps the Worker on the account's existing
   `workers.dev` subdomain without an email address, installs the
   InstaBox-generated HMAC secret, then deploys `wrangler-probe.toml` with the
   email address rule. On later deployments, the probe config preserves the
   existing rule while enabling the temporary HTTP health endpoint.
6. A signed health request invokes the actual Cloudflare Worker runtime. The
   Worker calls the public Testagram API and requires the native endpoint
   marker plus the expected unsigned-request HTTP 401. The Worker returns
   HTTP 204 only if that subrequest succeeds.
7. The script deploys the final `wrangler.toml` configuration with
   `workers_dev=false`, `preview_urls=false`, and the literal `b@bounces.testagram.site` email rule.
   It reapplies the HMAC secret, verifies the email rule targets this Worker,
   and confirms the temporary `workers.dev` HTTP endpoint and version preview URLs are disabled. Only
   then does InstaBox re-enable the outbox sender and restart the API; queued
   messages resume through Testagram's native SMTP transport.

The Cloudflare account must already have a `workers.dev` subdomain configured
under Workers & Pages. The deployment reads that account subdomain but does
not create or rename it. If the account has no `workers.dev` subdomain, the
deployment fails before it registers the email address route.

## Bounce security and processing

Testagram creates a per-message envelope sender:
`b+<32-character-base64url-token>@bounces.testagram.site`.
Cloudflare subaddressing maps it to the base Worker address while preserving
the original recipient in the email event.

The Worker requires Cloudflare's prepended
`Authentication-Results: mx.cloudflare.net;` header with `dmarc=pass`; it
does not trust sender-supplied authentication-result headers. It preserves
the case-sensitive VERP token, lowercases only the DNS domain, reads the raw
MIME stream, and enforces a 1 MiB limit.

It signs timestamp, canonical recipient, and SHA-256 body digest with
HMAC-SHA-256, then posts the original bytes to
`POST /internal/mail/bounce` over HTTPS. Testagram verifies the signature and
five-minute timestamp window, parses DSN/ARF, validates the token and
recipient fingerprint against PostgreSQL, and applies existing idempotent
delivery-state and suppression logic.

The temporary HTTP health endpoint is HMAC-protected and is disabled after
deployment. The final Worker is configured for email events only.

Permanent API rejections (400, 413, 422) are rejected at SMTP level; authentication failures, rate limits, server errors and network failures throw rather than being acknowledged. Cloudflare's SMTP retry behavior for Worker failures must be verified with a controlled outage test; do not assume retries without evidence. Cloudflare validates inbound SPF/DKIM/DMARC before invoking the
Worker, so real Gmail and Outlook DSNs must be tested through the full path.

## Production acceptance

- [ ] InstaBox confirms stable, dedicated outbound IPv4 and configurable PTR.
- [ ] Outbound TCP/25 connects to Gmail and Outlook MX hosts from InstaBox.
- [ ] `mail.testagram.site` A and PTR match the actual egress IPv4.
- [ ] `bounces.testagram.site` MX remains Cloudflare-owned.
- [ ] Exactly one SPF record authorizes InstaBox and Cloudflare Email Routing.
- [ ] Testagram `tg2026` DKIM verifies on real outbound messages.
- [ ] DMARC policy is preserved and recipient results show SPF/DKIM/DMARC pass.
- [ ] Worker-to-API health probe passes and temporary `workers.dev` URL is off.
- [ ] Cloudflare email rule receives a generated plus-address.
- [ ] Worker/API outage behavior is tested; no bounce is silently lost.
- [ ] Real Gmail and Outlook messages reach the intended inboxes.
- [ ] A real hard-bounce DSN suppresses once; replay creates no duplicate event.

CI and Wrangler dry-runs validate code and configuration only. They do not
prove production SMTP reachability, DNS correctness, inbox placement, or real
bounce delivery.