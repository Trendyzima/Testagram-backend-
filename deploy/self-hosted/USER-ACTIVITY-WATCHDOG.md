# Testagram authenticated-activity watchdog

The Backend control plane exposes an internal-only impulse queue for the InstaBox watchdog. It is not a public session endpoint.

## Trigger policy

- A successful login emits `login` and requests the Algorithm service.
- `feed_open` requests Algorithm.
- `post_publish` requests Data Centre.
- `media_upload` and `media_view` request CDN and Data Centre.
- `federation_interaction` requests Federation.
- Backend/API authentication and PostgreSQL remain always-on. The API repository is a contract/control-plane source, not an independently awakened runtime process.

## Trust boundary

The application must emit an impulse only after its existing production authentication middleware has validated the session and authorized the action. The client must never call these endpoints directly. Do not send user IDs, email addresses, access tokens, session cookies, or other identity data in an impulse. Use a cryptographically random per-event idempotency key (at least 16 characters).

The control-plane endpoints require the existing internal bearer token:

- `POST /v1/watchdog/impulses` — body: `{"idempotencyKey":"...","eventType":"login"}`; returns accepted event and mapped services.
- `POST /v1/watchdog/impulses/claim?limit=10` — trusted activation worker claims queued events; 60-second lease; expired claims can be retried.
- `POST /v1/watchdog/impulses/ack` — body: `{"id":123,"success":true}` or a bounded error string.
- `GET /v1/watchdog/services` — active service leases.

All events are idempotent and persisted in PostgreSQL. Service leases are extended by new events for ten minutes. Claim operations use row locking with `SKIP LOCKED` to support multiple workers. Only fixed service names can be selected; event payloads cannot supply arbitrary commands, images, URLs, or service names.

## Deployment and remaining integration

Apply migration `20261115_instabox_user_impulse_watchdog.sql` before enabling the endpoints. The existing public authentication and health routes are unchanged.

This PR implements the authenticated internal event queue and lease/claim contract. It does **not** claim to start containers by itself: a trusted InstaBox/node activation worker must consume claims and call the existing allowlisted runtime actuator. The production auth/API request handlers must also emit the corresponding impulse after successful authentication. Until those two integrations are deployed and tested, leave the service auto-suspend policy unchanged and do not disable existing services.
