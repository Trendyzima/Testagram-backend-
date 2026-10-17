# Queue consumer contract

The private platform queue uses Valkey Streams and provides **at-least-once** delivery. It is not an exactly-once job system.

## API contract

- `POST /v1/queue/enqueue` accepts `{ "queue": "name", "payload": { "job_id": "stable-id", "type": "domain.action" } }`.
- `POST /v1/queue/claim` returns `{ "messages": [{ "id": "stream-id", "values": { "job_id": "stable-id" } }] }`.
- `GET /v1/queue/reclaim` returns the same normalized message shape plus `next_start` for cursor pagination. Pass that cursor back as `start` to continue scanning pending entries.
- `POST /v1/queue/ack?queue=...&group=...&id=...` acknowledges one message.

All endpoints require the private platform bearer token and must remain inaccessible to browser/mobile clients.

## Worker behavior

`internal/queueworker` implements a reusable worker loop. It reclaims abandoned pending messages before claiming new ones, acknowledges successful work only after the handler returns, and preserves a stable `__testagram_job_id` across retry stream entries. Handlers must use that stable ID (or the product's own durable unique ID) for idempotency.

Handler failures are re-enqueued with a bounded `__testagram_attempt` counter. Once the configured attempt limit is reached, the message is moved to `<queue>.dead`. The original pending message is acknowledged only after the retry/dead-letter enqueue succeeds. If acknowledgement fails after enqueue, duplicates are possible by design; idempotency is mandatory.

## Production integration gates

This package is a transport/retry foundation, not proof that product jobs are integrated. Before enabling a production worker, each queue needs an explicit handler and contract tests for its domain side effects:

- `federation`: signature validation, activity deduplication, bounded remote HTTP timeouts, retry classification, and delivery idempotency.
- `notifications`: durable recipient identity, dedupe key, provider failure classification, and privacy-safe logs.
- `media`: upload state transition, object ownership, checksum/size/type validation, and transcode completion callbacks.
- `search`: idempotent indexing, delete/update ordering, and a repair/reindex path.

Dead-letter queues need an operator inspection/replay procedure and age/length alerts. Do not automatically delete unacknowledged stream entries. The current worker package does not yet supply domain-specific handlers or a standalone worker deployment; do not treat the package alone as a production consumer.