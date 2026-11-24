# Testagram inbound bounce worker

This Worker is an **inbound-only** adapter. It receives mail for Testagram VERP
return paths and posts the original RFC 5322 message to Testagram's HTTPS API.
It does not send application email and is not an SMTP relay.

## Cloudflare setup

1. In Cloudflare Email Routing, enable the `bounces.testagram.site` subdomain
   under the `testagram.site` zone. This is a subdomain-only setup; do not
   replace the zone apex MX records used by any existing mailbox provider.
2. Enable Email Routing subaddressing for the zone. Testagram generates
   recipients in the form `b+<32-character-token>@bounces.testagram.site`;
   subaddressing maps them to the Worker rule for `b@bounces.testagram.site`
   while preserving the full address in the Worker's `message.to` value.
3. Install Wrangler and validate the Worker configuration:
   `npm install --global wrangler`
   `wrangler deploy --dry-run`
4. Create a Cloudflare API token for deployment with Workers Scripts Write
   and Email Routing Rules Write permissions, scoped to the account and
   `testagram.site` zone. Do not reuse the DNS-only token.
5. Set Worker secrets/variables:
   - `TESTAGRAM_BOUNCE_INGRESS_URL`: the public HTTPS URL for
     `POST /internal/mail/bounce` on the Testagram API.
   - `TESTAGRAM_BOUNCE_INGRESS_SECRET`: a randomly generated 32-byte-or-longer
     secret. It must exactly match the value configured for the Testagram API.
6. Deploy from this directory with `wrangler deploy`. The configured
   `addresses` entry creates the literal base-address Worker rule. Confirm
   subaddressing is enabled before sending a test DSN.

## Security and delivery behavior

- The Worker validates the VERP recipient shape and enforces a 1 MiB message
  limit before forwarding.
- It preserves the original raw MIME bytes and signs the timestamp, normalized
  recipient, and SHA-256 body digest with HMAC-SHA-256.
- The API rejects missing/invalid signatures and requests outside a five-minute
  clock window. The API independently parses the DSN, validates the token
  against PostgreSQL, verifies the original recipient fingerprint, and uses
  the existing report hash for idempotency.
- A non-2xx API response throws from the Worker handler so the event is not
  silently acknowledged as processed.
- No message content, recipient, token, or secret is written to Worker logs.

## Test boundary

A successful Worker deployment and a successful HTTP response are not proof
that an actual bounce was delivered. Acceptance requires a controlled message
to a generated VERP address, a real DSN arriving through Cloudflare Email
Routing, exactly one correlated Testagram delivery event, and a replay test.
