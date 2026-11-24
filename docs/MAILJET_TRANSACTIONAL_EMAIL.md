# Testagram transactional email — Mailjet HTTPS Send API

Testagram sends authentication and consent-based social notification email from the Backend running on InstaBox. Cloudflare remains the DNS provider; Cloudflare Email Sending is not used.

## Runtime configuration

Set these values as GitHub production environment secrets/variables (never commit real values):

- `MAILJET_HTTPS Send API_USERNAME`: Mailjet API key.
- `MAILJET_HTTPS Send API_PASSWORD`: Mailjet Secret Key.
- `MAILJET_HTTPS Send API_PORT`: `587` only; the Backend requires HTTPS/TLS.
- `AUTH_FROM_EMAIL`: `no-reply@testagram.site`.
- `TESTAGRAM_MAIL_ENCRYPTION_KEY`: 32 random bytes encoded as 64 hexadecimal characters.

The deployment workflow forwards the credentials over the authenticated InstaBox deployment control plane, writes them to the VPS environment file, and verifies the configured API container environment without printing credential values. The VPS environment file must remain mode 0600.

## Mailjet account onboarding

1. Add and validate `testagram.site` in Mailjet under Account Settings → Senders & Domains.
2. Copy the exact SPF and DKIM DNS records supplied by Mailjet into the Cloudflare DNS zone. Merge SPF authorization into an existing SPF record; do not publish multiple SPF records at the root.
3. Wait for Mailjet to verify the domain and confirm that `no-reply@testagram.site` is an allowed sender.
4. In HTTPS Send API and SEND API settings, use the API key as the HTTPS Send API username and Secret Key as the password.

Mailjet HTTPS Send API host is `api.mailjet.com/v3.1/send`; Testagram uses port 587 with HTTPS/TLS. The application refuses non-TLS port configuration.

## Delivery event callback

Configure Mailjet Event API callbacks for `sent`, `bounce`, `blocked`, and `spam`, with grouped delivery (Version 2), pointing to:

`https://<URL_ENCODED_MAILJET_API_KEY>:<URL_ENCODED_MAILJET_SECRET_KEY>@api.testagram.site/internal/mailjet/events`

The callback uses HTTP Basic authentication with the Mailjet API key and Secret Key (URL-encode both values when necessary). Keep the URL private; do not paste it into tickets, source code, or public chat. Mailjet sends JSON event batches. Testagram only ingests messages tagged with its `X-MJ-CustomID` header and ignores unrelated Mailjet account traffic.

The endpoint persists events idempotently. The outbox worker reconciles sent, hard-bounce, blocked-recipient, and spam-complaint events against the exact queued message and recipient fingerprint. Hard bounces, recipient blocks, and spam complaints create recipient suppressions. Soft bounces do not create permanent suppressions.

## Acceptance sequence

1. Confirm Mailjet domain and sender validation.
2. Configure the production secrets above.
3. Run the read-only deployment validation and deploy the code.
4. Register the Event API callback and verify authenticated event ingestion.
5. Obtain explicit approval for one controlled OTP send.
6. Confirm HTTPS Send API acceptance, Mailjet event delivery, and actual inbox receipt.
7. Verify password recovery and consenting follow/post notifications, including retries and duplicate protection.

A successful HTTPS Send API DATA response means the relay accepted the message; it is not proof that the recipient's inbox received it. Do not mark production email acceptance complete until the actual OTP and subsequent event checks pass.


## InstaBox network requirement

The production worker uses Mailjet Send API v3.1 over HTTPS (`https://api.mailjet.com/v3.1/send`, TCP 443). It does not require SMTP egress on ports 587 or 2525. The deployment performs a read-only verified TLS handshake to `api.mailjet.com:443` before changing production services. Existing GitHub production secrets `MAILJET_API_KEY` and `MAILJET_SECRET_KEY` are forwarded as runtime API credentials; no credential rotation is required. The worker retains the encrypted outbox, Mailjet `CustomID` event correlation, conservative ambiguous-outcome quarantine, consent checks, and retry policy. A successful API acceptance is not proof of inbox delivery; verify the Mailjet lifecycle event and actual recipient inbox.
