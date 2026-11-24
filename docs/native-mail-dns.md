# Testagram native mail DNS

The native mail transport sends directly from the InstaBox VPS. It uses
`mail.testagram.site` as its SMTP EHLO hostname, signs messages with DKIM
(`d=testagram.site`, selector `tg2026`), and uses per-message VERP return
addresses at `bounces.testagram.site`.

Do not publish placeholder IP addresses or placeholder DKIM keys. The deployment
script creates the private key on InstaBox and prints the matching public key. It also generates a separate
`TESTAGRAM_MAIL_VERP_KEY` on the VPS; this is distinct from the encrypted-outbox
key and is never supplied by an external mail provider. Keep the VERP key stable
while any return-path tokens remain valid (45-day token lifetime plus the
retention window); rotating it early invalidates pending bounce correlations. Inbound
SMTP advertises STARTTLS when a valid certificate and key are installed at
`/data/home/testagram/mail-tls/fullchain.pem` and
`/data/home/testagram/mail-tls/privkey.pem`.
Publish the records only after confirming the VPS public IPv4 address and that
all outbound mail for the bounce subdomain comes from this VPS.

## Required records

| Type | Name | Value |
|---|---|---|
| A | `mail.testagram.site` | The actual InstaBox public IPv4 address |
| MX | `bounces.testagram.site` | Priority 10, target `mail.testagram.site.` |
| TXT | `bounces.testagram.site` | `v=spf1 ip4:<INSTABOX_PUBLIC_IPV4> ~all` during initial rollout; tighten to `-all` only after confirming no other senders use the bounce subdomain. |
| TXT | `tg2026._domainkey.testagram.site` | `v=DKIM1; k=rsa; p=<PUBLIC_KEY_FROM_DEPLOY_LOG>` |
| TXT | `_dmarc.testagram.site` | `v=DMARC1; p=none; adkim=r; aspf=r; rua=mailto:<MONITORED_REPORT_MAILBOX>` |

The SPF record belongs on **bounces.testagram.site**, because the SMTP MAIL FROM
address uses that subdomain. Publish exactly one SPF record at that name. The
`-all` policy is appropriate only after confirming there are no other legitimate
senders using `bounces.testagram.site`.

The DMARC report mailbox must exist and be monitored before it is placed in the
`rua` tag. Begin with `p=none`; move to quarantine/reject only after reviewing
aggregate reports and verifying alignment.

## Reverse DNS and connectivity

- Set the VPS provider's PTR/rDNS for the sending IPv4 address to
  `mail.testagram.site`.
- Ensure `mail.testagram.site` resolves forward to that same IPv4 address.
- Permit outbound TCP/25 from InstaBox to recipient MX hosts.
- Permit inbound TCP/25 to InstaBox and forward it to the API container's
  feedback listener on port 2525.
- Install a valid certificate for `mail.testagram.site` in the two VPS paths above;
  the deployment script mounts them read-only and enables STARTTLS automatically.
  Without those files, the receiver deliberately does not advertise STARTTLS.
- Confirm the provider does not block port 25 in either direction.

Do not proxy SMTP through an HTTP/CDN proxy. The MX target must resolve directly
to the VPS address.

## Acceptance

After records have propagated, verify:

1. `dig A mail.testagram.site`, `dig MX bounces.testagram.site`, and the three
   TXT records above.
2. `dig -x <INSTABOX_PUBLIC_IPV4>` returns `mail.testagram.site.`.
3. A controlled message to Gmail and Outlook has `spf=pass`,
   `dkim=pass`, and `dmarc=pass` in the receiving Authentication-Results.
4. A controlled hard-bounce DSN to a generated VERP address is correlated to
   exactly one outbox row and creates a hard-bounce suppression.
5. Replaying the same DSN does not create a second event or suppression.

A recipient MX accepting a message with SMTP 250 is not proof of inbox placement.
