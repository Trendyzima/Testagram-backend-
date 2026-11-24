# Testagram native mail on InstaBox

Testagram owns outbound mail delivery, signing, queueing, retries, VERP tokens,
bounce correlation, suppression, and delivery state. The application and native
SMTP sender remain on InstaBox. There is no Mailjet, Resend, or external
outbound SMTP relay.

Cloudflare Email Routing is approved for one narrow purpose only: receiving
messages sent to `bounces.testagram.site` and invoking the Testagram bounce
Worker. The Worker forwards the original MIME message to the Testagram HTTPS
API using a shared HMAC secret. It does not send application mail.

## Mail flow

1. The Testagram outbox worker on InstaBox resolves the recipient MX and
   connects directly to that MX over TCP/25 with STARTTLS.
2. Testagram signs the message with its own DKIM key (`d=testagram.site`,
   selector `tg2026`). The envelope sender is a per-message VERP address:
   `b+<token>@bounces.testagram.site`.
3. The recipient MX returns a DSN to the VERP address. Cloudflare Email
   Routing receives it on the `bounces.testagram.site` subdomain and invokes
   the Email Worker using the `b@bounces.testagram.site` base route plus
   addressing.
4. The Worker sends the original RFC 5322 bytes to
   `POST /internal/mail/bounce` over HTTPS. It authenticates the timestamp,
   recipient and body digest with HMAC-SHA-256.
5. Testagram independently verifies the signature and timestamp, parses the
   DSN/ARF, checks the VERP token and recipient fingerprint against
   PostgreSQL, and applies its existing idempotent event, delivery-state and
   suppression logic.

Cloudflare Email Routing must be enabled only for the `bounces.testagram.site`
subdomain. Do not onboard Email Routing on the zone apex as part of this
change, and do not alter the root MX records used by any existing mailbox
provider.

## InstaBox outbound network acceptance

InstaBox's published-port gateway cannot expose inbound TCP/25, which is why
the bounce receiver is now HTTP-ingress-only through Cloudflare. This does
not establish whether outbound TCP/25 is permitted or whether the outbound
source IP is suitable for direct-to-MX delivery.

The deployment preflight attempts TCP/25 to both Gmail and Outlook MX hosts
and fails closed if either connection cannot be established. This is only a
basic egress test. Before production acceptance, independently establish:

- The actual public IPv4 used by InstaBox for outbound SMTP.
- That the egress IPv4 is stable and dedicated or otherwise explicitly
  permitted for direct SMTP by InstaBox.
- That the provider can set PTR/rDNS for that IPv4 to `mail.testagram.site`.
- That `mail.testagram.site` resolves forward to the same IPv4.
- That the InstaBox machine remains running continuously and the sender is
  not suspended while the outbox has pending mail.

If InstaBox uses shared or changing egress without a configurable matching
PTR, direct-to-MX delivery may be rejected or heavily filtered even when
TCP/25 is reachable. Do not claim Gmail/Outlook acceptance until actual
messages pass SPF, DKIM and DMARC and reach the intended inboxes.

## Cloudflare inbound setup

Cloudflare Email Service requires Cloudflare DNS for the zone. Enable the
`bounces.testagram.site` subdomain under Email Routing settings for
`testagram.site`; do not enable Email Routing on the apex domain. Cloudflare
will provision the subdomain's inbound MX and authentication records.

Enable Email Routing subaddressing. Testagram's VERP recipients use
`b+<token>@bounces.testagram.site`; subaddressing maps them to the Worker rule
for `b@bounces.testagram.site` while preserving the full recipient in
`message.to`. The Worker configuration declares that base address.

Deploy `cloudflare/email-bounce-worker` with Wrangler. The deployment token
requires Workers Scripts Write and Email Routing Rules Write permissions.
Do not reuse the DNS-only API token. Configure:

- `TESTAGRAM_BOUNCE_INGRESS_URL`: the public HTTPS URL of the Testagram API
  endpoint `POST /internal/mail/bounce`.
- `TESTAGRAM_BOUNCE_INGRESS_SECRET`: the same randomly generated secret as
  `TESTAGRAM_MAIL_BOUNCE_INGRESS_SECRET` on InstaBox. The InstaBox deployment
  generates and persists this 32-byte key automatically in its protected
  `.env` file; the Worker secret must be provisioned with the matching value.

The Worker rejects malformed VERP recipients and messages over 1 MiB. It
does not log message bodies, recipient tokens or secrets. It throws on any
non-2xx response from Testagram so a failed handoff is not acknowledged as
success.

## DNS records and authentication

Do not run the old dedicated-host DNS reconciler against this InstaBox
deployment. Cloudflare Email Routing owns the inbound MX records on
`bounces.testagram.site`; replacing them with `mail.testagram.site` would
break bounce reception.

After confirming the real InstaBox outbound IPv4 and PTR, configure the
outbound identity records without replacing Cloudflare's inbound records:

| Type | Name | Value |
|---|---|---|
| A | `mail.testagram.site` | Confirmed stable InstaBox SMTP egress IPv4 |
| MX | `bounces.testagram.site` | Cloudflare Email Routing MX targets provisioned for the subdomain |
| TXT | `bounces.testagram.site` | One merged SPF record: `v=spf1 ip4:<INSTABOX_EGRESS_IPV4> include:_spf.mx.cloudflare.net ~all` (adjust only after validating all senders) |
| TXT | `tg2026._domainkey.testagram.site` | `v=DKIM1; k=rsa; p=<PUBLIC_KEY_FROM_TESTAGRAM_DKIM_KEY>` |
| TXT | `_dmarc.testagram.site` | Preserve the existing DMARC policy; begin with monitoring if no policy exists |

Cloudflare Email Routing also provisions its own DKIM selector for routed
messages. Keep that selector and its records separate from Testagram's
`tg2026` DKIM selector. Merge SPF mechanisms into a single SPF TXT record at
`bounces.testagram.site`; never publish two independent SPF records there.

The `mail.testagram.site` A record and matching PTR are required for a
trustworthy direct-SMTP identity. If InstaBox cannot provide a stable egress
IPv4 with configurable reverse DNS, this remains a production blocker; do
not substitute an outbound IP-echo result or a shared NAT address without
provider confirmation.

## Secrets and key lifecycle

`TESTAGRAM_MAIL_ENCRYPTION_KEY`, `TESTAGRAM_MAIL_VERP_KEY`, and
`TESTAGRAM_MAIL_BOUNCE_INGRESS_SECRET` are separate internally generated
secrets persisted by the InstaBox deployment. No SMTP-provider credentials
are required. Keep the VERP key stable while any issued return-path tokens
remain valid. Keep the ingress HMAC secret identical between the API and
Worker; rotate it as a coordinated change.

The DKIM private key remains Testagram-owned and is mounted read-only into
the API container. Publish only its public key. Never commit private keys or
the ingress secret.

## Production acceptance checklist

- [ ] InstaBox deployment preflight confirms outbound TCP/25 to Gmail and
      Outlook MX hosts.
- [ ] InstaBox confirms stable SMTP egress IPv4 and matching PTR/rDNS.
- [ ] `mail.testagram.site` A and PTR match.
- [ ] Cloudflare Email Routing is enabled only for `bounces.testagram.site`;
      apex MX records remain unchanged.
- [ ] Subaddressing is enabled and `b@bounces.testagram.site` routes to the
      deployed Worker.
- [ ] Worker and InstaBox API share the same ingress HMAC secret.
- [ ] Controlled Gmail and Outlook deliveries show `spf=pass`, `dkim=pass`,
      and `dmarc=pass` in recipient Authentication-Results and reach inboxes.
- [ ] A real hard-bounce DSN arrives through Cloudflare, correlates to one
      outbox row, and creates the expected suppression.
- [ ] Replaying the same DSN creates no duplicate event or suppression.

CI passing is code validation, not proof of live SMTP delivery or bounce
ingestion. Keep PR #536 a draft until the live acceptance checklist passes.