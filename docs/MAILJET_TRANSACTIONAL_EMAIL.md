# Testagram transactional email — Mailjet Send API v3.1

Testagram sends authentication and consent-based social notification email from the Backend running on InstaBox. Cloudflare remains the DNS provider; Cloudflare Email Sending is not used.

## Transport and runtime configuration

Production uses Mailjet Send API v3.1 over HTTPS:

- Endpoint: `https://api.mailjet.com/v3.1/send`
- Network: outbound TCP 443 (HTTPS/TLS)
- Authentication: HTTP Basic Auth
- Username: `MAILJET_API_KEY` (Mailjet public API key)
- Password: `MAILJET_SECRET_KEY` (Mailjet private Secret Key)

The application does not use SMTP and does not require outbound TCP 587 or 2525. The InstaBox deployment performs a read-only, certificate-verified TLS handshake to `api.mailjet.com:443` before modifying production services. This check sends no email and does not expose credentials.

Set these GitHub production environment secrets/variables; never commit real values:

- `MAILJET_API_KEY`: Mailjet public API key.
- `MAILJET_SECRET_KEY`: Mailjet private Secret Key.
- `AUTH_FROM_EMAIL`: `no-reply@testagram.site` (must be validated and active in Mailjet).
- `TESTAGRAM_MAIL_ENCRYPTION_KEY`: 32 random bytes encoded as 64 hexadecimal characters.

The existing Mailjet API credentials are reused; no SMTP credential or port setting is needed. The deployment forwards the credentials over the authenticated InstaBox control plane, writes them to the VPS environment file, and verifies the configured API container environment without printing credential values. The VPS environment file must remain mode 0600.

## Mailjet account onboarding

1. Add and validate `testagram.site` in Mailjet under Account Settings → Senders & Domains.
2. Copy the exact SPF and DKIM DNS records supplied by Mailjet into the Cloudflare DNS zone. Merge SPF authorization into an existing SPF record; do not publish multiple SPF records at the root.
3. Wait for Mailjet to verify the domain and confirm that `no-reply@testagram.site` is an allowed sender.
4. Confirm that the API key and Secret Key are active for the Mailjet account.

## Delivery event callback

Configure Mailjet Event API callbacks for `sent`, `bounce`, `blocked`, and `spam`, with grouped delivery (Version 2), pointing to:

`https://<URL_ENCODED_MAILJET_API_KEY>:<URL_ENCODED_MAILJET_SECRET_KEY>@api.testagram.site/internal/mailjet/events`

The callback uses HTTP Basic authentication with the Mailjet API key and Secret Key (URL-encode both values when necessary). Keep the URL private; do not paste it into tickets, source code, or public chat. Mailjet sends JSON event batches. Testagram only ingests messages tagged with its `CustomID` field and ignores unrelated Mailjet account traffic.

The endpoint persists events idempotently. The outbox worker reconciles sent, hard-bounce, blocked-recipient, and spam-complaint events against the exact queued message and recipient fingerprint. Hard bounces, recipient blocks, and spam complaints create recipient suppressions. Soft bounces do not create permanent suppressions.

## Delivery and duplicate protection

Each Send API request carries a unique Testagram `CustomID` (within Mailjet's 64-character limit). The worker stores that identifier in the outbox for matching Mailjet Event API callbacks. Explicit API rejections are handled according to the bounded retry policy. Network timeouts, HTTP 408/5xx responses, malformed success responses, or missing provider identifiers are treated as ambiguous outcomes and quarantined as `uncertain`; they are not automatically resent. This avoids duplicate OTP/security messages if Mailjet accepted a request but the response was lost.

The encrypted outbox, recipient suppression checks, consent checks immediately before social sends, unsubscribe headers, event de-duplication, and delivery reconciliation remain in place. A successful Send API response means Mailjet accepted the message; it is not proof that the recipient's inbox received it.

## Production acceptance sequence

1. Confirm Mailjet domain and sender validation.
2. Confirm the GitHub production secrets above are configured.
3. Run the read-only deployment validation and deploy the code.
4. Register the Event API callback and verify authenticated event ingestion.
5. Obtain explicit approval for one controlled OTP send.
6. Confirm HTTPS Send API acceptance, the corresponding Mailjet event, and actual inbox receipt.
7. Verify password recovery and consent-based follow/post notifications, including retry and duplicate protection.

Do not mark production email acceptance complete until actual OTP delivery and subsequent event checks pass.
