# Testagram unified notification engine

## Ownership and request path

The Backend PostgreSQL database is the single source of truth for notification events, the in-app inbox, read state, category preferences and channel delivery state.

- Business handlers publish `testagram_local.notification_events` in the same transaction as the business action.
- The publisher inserts the in-app read model into `testagram_local.notifications` and creates channel delivery rows in `testagram_local.notification_deliveries`.
- Event keys are unique per recipient, making business retries and webhook replays idempotent.
- Backend serves both the REST inbox and authenticated SSE stream directly from its own database. There is no cross-service notification database dependency.
- Flutter calls only the public Backend API with its native session. The stream is a read-only projection and never marks inbox items read.
- Data Centre remains responsible for its separate storage and media lifecycle domains; it is not a second notification source of truth.

## Live stream behavior

The Backend exposes `GET /api/social/notifications/stream`. It validates the native user session, then reads only rows for that authenticated recipient from `testagram_local.notifications`.

The stream supports opaque replay cursors through `Last-Event-ID`, ordered by `created_at,id`. Connections are bounded to 25 seconds to respect the API server write deadline. Flutter reconnects and supplies the last cursor. The stream does not mutate the notification ledger or read state.

The REST inbox remains the recovery path if SSE is interrupted. A stream frame is only a signal that the inbox may have changed; Flutter refreshes the authoritative REST projection and marks items read through the Backend.

## Channel dispatch status

The event ledger and in-app inbox projection are implemented in the review candidate. Push delivery still requires a worker that claims `push` delivery rows, authenticated Android/iOS token registration, server-side FCM/APNs provider configuration, retries/dead-letter handling and real-device acceptance. Email dispatch integration remains a separate workstream. Do not mark a delivery sent merely because it was enqueued or because an SSE frame was written.

## Operational acceptance

Before production acceptance, test:

1. One event committed with its originating transaction and no duplicate after replay.
2. Two different recipients cannot see each other's events.
3. SSE reconnect replays only rows after the supplied cursor and never marks them read.
4. Read state remains authoritative in Backend after SSE delivery.
5. Backend stream outage leaves the REST inbox usable and causes bounded client reconnects.
6. Push retries, provider token invalidation and worker lease recovery are tested separately.
