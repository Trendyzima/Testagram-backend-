# Testagram unified notification engine

## Ownership

The Backend PostgreSQL database is the single source of truth for notification events, the in-app inbox, read state, category preferences and channel delivery state.

- Business handlers publish `testagram_local.notification_events` in the same transaction as the business action.
- The publisher inserts the in-app read model into `testagram_local.notifications` and creates channel delivery rows in `testagram_local.notification_deliveries`.
- Event keys are unique per recipient, making business retries and webhook replays idempotent.
- The Data Centre notification service is a read-only real-time delivery projection. Its new `GET /v2/stream` endpoint reads `testagram_local.notifications`; it does not create events, change read state or own a second canonical queue.
- Backend exposes `GET /api/social/notifications/stream` and forwards the authenticated native session bearer only to the private Data Centre service. Flutter never receives or calls the internal service URL.
- Flutter continues to load the canonical inbox over `GET /api/social/notifications`, marks items read through the Backend, and uses SSE only as a prompt to refresh.

## Private stream configuration

Set `DATA_CENTRE_NOTIFICATION_STREAM_URL` on the Backend API to the Data Centre notification-service origin reachable on the private platform network, for example `http://notification:8090`. Do not publish this service port to the Internet and do not place this URL in Flutter configuration.

The Data Centre notification service must use the same Testagram PostgreSQL database as Backend, and its database role must have SELECT access to `testagram_local.notifications`. Verify both conditions in staging before enabling the stream.

Backend bounds each proxied stream connection to 25 seconds to respect the API server's global write deadline. Data Centre emits keep-alive comments; Flutter reconnects and supplies the opaque `Last-Event-ID` cursor. The cursor is ordered by `created_at,id` and is not a user credential.

## Channel dispatch status

The event ledger and in-app inbox projection are implemented in the review candidate. Push delivery still requires a worker that claims `push` delivery rows, authenticated Android/iOS token registration, server-side FCM/APNs provider configuration, retries/dead-letter handling and real-device acceptance. Email dispatch integration remains a separate workstream. Do not mark a delivery sent merely because it was enqueued or because an SSE frame was written.

## Operational acceptance

Before production acceptance, test:

1. One event committed with its originating transaction and no duplicate after replay.
2. Two different recipients cannot see each other's events.
3. SSE reconnect replays only rows after the supplied cursor and never marks them read.
4. Read state remains authoritative in Backend after SSE delivery.
5. Data Centre outage leaves the inbox REST API usable and causes bounded client reconnects.
6. Backend stream proxy never redirects the user's bearer to another host.
7. Push retries, provider token invalidation and worker lease recovery are tested separately.
