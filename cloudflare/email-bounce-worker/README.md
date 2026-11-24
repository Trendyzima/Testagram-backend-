# Testagram inbound bounce Worker

This Worker is an **inbound-only** adapter. It receives DSNs sent to Testagram
VERP return paths and posts the original RFC 5322 message to Testagram on
InstaBox over HTTPS. It does not send application mail and is not an SMTP relay.

## One-time Cloudflare setup

1. In Cloudflare, open `testagram.site` → Compute → Email Service → Email
   Routing → Settings → Subdomains. Add `bounces.testagram.site` there.
   Cloudflare provisions the subdomain's inbound MX, SPF and routing DKIM
   records. Do not enable Email Routing on the apex as part of this change,
   and do not change the existing `testagram.site` apex MX records.
2. Review existing Email Routing rules, then enable subaddressing in Email
   Routing → Settings. This is a **zone-wide** setting and affects plus-address
   matching for every existing apex and subdomain rule, not only Testagram's
   bounce address. The deployment checks that it is already enabled; it does
   not toggle this setting or rewrite apex MX records.
3. Create a dedicated Cloudflare API token and save it as the GitHub Actions
   production secret `CLOUDFLARE_BOUNCE_WORKER_API_TOKEN`. This is Cloudflare control-plane access only, not an SMTP or outbound mail credential. Required scopes:
   - Account: Workers Scripts Read and Write
   - Zone `testagram.site`: Zone Read
   - Zone `testagram.site`: Zone Settings Read
   - Zone `testagram.site`: Email Routing Rules Read and Write
4. Keep the existing DNS-only token in GitHub secret
   `CLOUDFLARE_DNS_API_TOKEN` with Zone Read and DNS Edit scoped to
   `testagram.site`. It is used only for outbound A/SPF/DKIM/DMARC records;
   it is not reused to deploy Workers.
5. Ensure GitHub secret `CLOUDFLARE_ACCOUNT_ID` identifies the account
   containing the `testagram.site` zone and the Worker.
6. After InstaBox confirms its stable, dedicated SMTP egress IPv4 and the
   provider-configured PTR, set repository variable
   `TESTAGRAM_NATIVE_MAIL_EXPECTED_EGRESS_IPV4` to that exact IPv4. The
   deployment refuses to infer this value from an IP-echo service.

## Read-only InstaBox network diagnostic

After this workflow change is merged, run the `Deploy Testagram to InstaBox` workflow manually with `diagnose_native_mail_egress=true` and `confirm_production_deploy=false`. It executes a read-only command on the existing InstaBox machine and reports two public IPv4 observations, reverse PTR, the current `mail.testagram.site` A result, and TCP/25 connectivity to Gmail and Outlook MX hosts. It does not send email, change DNS, deploy containers, or issue machine start/stop commands; the machine must already be running. A single probe cannot prove the egress IP is static or dedicated; that still requires InstaBox provider confirmation.

## Automated production deployment

The InstaBox deployment performs the following in order:

1. Checks outbound TCP/25 to Gmail and Outlook MX hosts.
2. Cross-checks the actual egress IPv4 against two independent IP reflectors
   and verifies PTR equals `mail.testagram.site`. This is not a substitute
   for InstaBox confirming the address is static and dedicated.
3. Generates and persists Testagram's DKIM key and internal mail secrets.
4. Reconciles `mail.testagram.site` A (DNS-only), merges the InstaBox IPv4
   and Cloudflare Email Routing include into the single SPF record on
   `bounces.testagram.site`, publishes Testagram selector `tg2026`, and
   preserves any existing DMARC policy. It never changes the bounce MX;
   Cloudflare owns those inbound records.
5. Probes `https://api.testagram.site/internal/mail/bounce` and expects
   HTTP 401 without HMAC headers, proving the public route reaches the
   authenticated Testagram handler.
6. Enables plus-addressing, verifies Cloudflare MX exists on the bounce
   subdomain, deploys this Worker with Wrangler 4.113.0, and registers the
   literal `b@bounces.testagram.site` Worker rule.
7. Reads the InstaBox-generated HMAC secret from the protected InstaBox
   environment and sends it directly to Cloudflare with `wrangler secret
   put`. The secret is not printed, committed, or copied to GitHub Actions.

The deployment requires `npm`/Node.js 18 or later on the InstaBox machine.
If the bounce subdomain has not been enabled in Cloudflare first, deployment
fails before creating the Worker route.

## Mail flow and token integrity

1. Testagram creates a per-message envelope sender:
   `b+<32-character-base64url-token>@bounces.testagram.site`.
2. The recipient MX returns a DSN to Cloudflare's MX for the bounce
   subdomain. Cloudflare subaddressing matches the base rule while
   preserving the original recipient in `message.to`.
3. The Worker first requires Cloudflare's prepended `Authentication-Results: mx.cloudflare.net;` header with `dmarc=pass` with `dmarc=pass`; Cloudflare validates inbound SPF/DKIM/DMARC before invoking the Worker. It does not trust sender-supplied authentication-result headers. The Worker preserves the token's case-sensitive local part, lowercases
   only the DNS domain, reads the raw MIME stream, and enforces a 1 MiB cap.
4. It signs timestamp, canonical recipient and SHA-256 body digest using
   HMAC-SHA-256, then posts the raw bytes to Testagram over HTTPS.
5. Testagram validates the HMAC and five-minute timestamp window, parses the
   DSN/ARF, correlates the exact VERP token and recipient fingerprint in
   PostgreSQL, and applies existing idempotent delivery and suppression
   handling.

Do not lowercase the full VERP address: the base64url token is case-sensitive.
Repeated delivery of the same DSN must remain idempotent. The Worker throws
on non-2xx API responses rather than treating an unsuccessful handoff as
success. A controlled outage test must verify Cloudflare's SMTP retry behavior
before production acceptance; do not assume retries without that evidence.

Cloudflare Email Routing performs inbound SPF/DKIM/DMARC checks before
invoking a Worker. A real Gmail and Outlook DSN must therefore be tested
through the complete Cloudflare path, not merely by posting a synthetic MIME
message to the API.

## Production acceptance

- [ ] InstaBox confirms stable, dedicated outbound IPv4 and configurable PTR.
- [ ] Outbound TCP/25 connects to Gmail and Outlook MX hosts from InstaBox.
- [ ] `mail.testagram.site` A and PTR both match that egress IPv4.
- [ ] `bounces.testagram.site` MX remains Cloudflare-owned.
- [ ] Exactly one SPF record authorizes the InstaBox IPv4 and Cloudflare's
      Email Routing include.
- [ ] Testagram `tg2026` DKIM record resolves and verifies on a real message.
- [ ] Existing DMARC policy remains unchanged and recipient results show
      SPF/DKIM/DMARC pass.
- [ ] Cloudflare Worker route exists and receives a generated plus-address.
- [ ] Worker-to-API outage behavior is tested; no bounce is silently lost.
- [ ] Real Gmail and Outlook delivery reaches the intended inboxes.
- [ ] A real hard-bounce DSN correlates to one outbox row and suppresses the
      invalid recipient exactly once; replay creates no duplicate event.

CI validates code and deployment wiring only. It does not prove production
SMTP reachability, DNS correctness, inbox placement or real bounce delivery.