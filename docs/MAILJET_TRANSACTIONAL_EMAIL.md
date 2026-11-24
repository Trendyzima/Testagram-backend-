# Testagram transactional email — Mailjet SMTP

Testagram sends authentication and consent-based social notification email from the Backend running on InstaBox. Cloudflare remains the DNS provider; Cloudflare Email Sending is not used.

## Runtime configuration

Set these values as GitHub production environment secrets/variables (never commit real values):

- `MAILJET_SMTP_USERNAME`: Mailjet API key.
- `MAILJET_SMTP_PASSWORD`: Mailjet Secret Key.
- `MAILJET_SMTP_PORT`: `587` only; the Backend requires STARTTLS.
- `AUTH_FROM_EMAIL`: `no-reply@testagram.site`.
- `TESTAGRAM_MAIL_ENCRYPTION_KEY`: 32 random bytes encoded as 64 hexadecimal characters.

The deployment workflow forwards the credentials over the authenticated InstaBox deployment control plane, writes them to the VPS environment file, and verifies the configured API container environment without printing credential values. The VPS environment file must remain mode 0600.

## Mailjet account onboarding

1. Add and validate `testagram.site` in Mailjet under Account Settings → Senders & Domains.
2. Copy the exact SPF and DKIM DNS records supplied by Mailjet into the Cloudflare DNS zone. Merge SPF authorization into an existing SPF record; do not publish multiple SPF records at the root.
3. Wait for Mailjet to verify the domain and confirm that `no-reply@testagram.site` is an allowed sender.
4. In SMTP and SEND API settings, use the API key as the SMTP username and Secret Key as the password.

Mailjet SMTP host is `in-v3.mailjet.com`; Testagram uses port 587 with STARTTLS. The application refuses non-TLS port configuration.

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
6. Confirm SMTP acceptance, Mailjet event delivery, and actual inbox receipt.
7. Verify password recovery and consenting follow/post notifications, including retries and duplicate protection.

A successful SMTP DATA response means the relay accepted the message; it is not proof that the recipient's inbox received it. Do not mark production email acceptance complete until the actual OTP and subsequent event checks pass.
