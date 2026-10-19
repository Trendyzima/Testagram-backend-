# Dual-backend fault isolation (XClone hosted backend + Testagram VPS)

## Goal

Run the existing XClone backend and the self-hosted Testagram Go backend as independently operated services. They may cooperate through narrow, versioned APIs, but neither server's process, deployment, credentials, network, or availability is a prerequisite for the other's core operation. A failure or compromise of one must not automatically expose or corrupt the other service's data.

This is a design and rollout contract, not proof of a live failover test.

## Topology

- **Backend A — existing XClone backend:** existing Supabase Edge Functions/Postgres/Auth integration, Cloudflare services/R2 and current Redis/Upstash integrations remain in place. Do not replace or migrate existing content as part of this work.
- **Backend B — Testagram VPS:** self-hosted Go API, its own deployment and health checks, PostgreSQL/Supabase-compatible instance as configured, and private MinIO/object storage. It may optionally use Cloudflare R2 through its own credentials.
- **Client routing:** choose a backend per capability and per configured deployment mode. Do not make every API call depend on a single universal backend origin.
- **Cross-backend cooperation:** use explicit authenticated API calls and signed, short-lived, scoped transfer grants only when data actually needs to cross a boundary. Never give either server the other's master/service-role credentials.

## Failure model and behavior

1. If Testagram is unavailable, XClone-owned auth, feeds, search, posts, and legacy media continue through Backend A. Self-hosted-only operations show a clear degraded/unavailable state and can retry later.
2. If Backend A is unavailable, operations that are fully owned by Backend B continue only if its auth, database, and object store are independently healthy. Do not claim all XClone features survive if they depend on Backend A's data.
3. If either object store fails, only objects stored there are affected unless an explicitly verified replica exists. Do not silently return an empty feed or fabricate success.
4. Read failover is allowed only when the target has the authoritative object/record or a verified replica and compatible authorization. Write failover must not blindly retry against a second provider: it needs idempotency, a durable operation ID, and explicit reconciliation to prevent duplicate/orphan objects.
5. If a shared dependency fails (same Supabase project, DNS/CDN account, Redis instance, VPS provider, credentials or network), both services can still fail together. Independent servers alone do not guarantee fault isolation.

## Data and identity boundaries

- Keep existing Supabase rows and legacy media URLs authoritative; no bulk move, key rewrite or delete.
- Each backend verifies the identity token intended for it. If both accept the same issuer, configure explicit audience/issuer validation and least-privilege scopes; do not assume a token valid for one API is valid for the other.
- Use separate service credentials, storage buckets/access policies, database roles, network policies and deployment secrets.
- Redis/Upstash is cache/acceleration only; database transactions remain authoritative for ownership, quota and lifecycle.
- Cross-backend transfer is opt-in and auditable. Grant only one object, one operation, one destination, short expiry, and maximum bytes. Never forward a user bearer token to an unrelated origin.

## Health and routing

Expose separate readiness/liveness checks for each API and its own critical dependencies. A health endpoint must not leak secrets or detailed internal topology. Use bounded request timeouts, circuit breakers, limited retries with jitter, and request IDs. Health checks decide whether a service can receive its own traffic; they do not prove data replication or authorize cross-backend failover.

Client behavior:
- Keep Backend A as the default for existing features and data.
- Route only explicitly self-hosted capabilities to Backend B when configured and authenticated.
- On Backend B timeout, report that self-hosted functionality is temporarily unavailable; preserve drafts/retryable intent metadata where safe, but do not silently send private bytes to Backend A.
- Do not automatically change the authoritative write backend based only on health. A route change must be configured and idempotent.

## Security / compromise containment

- Isolate the VPS with firewall rules; expose only required HTTPS ports. Keep database, Redis, MinIO administration, metrics and orchestration control ports private.
- Use distinct credentials and rotate them independently. No shared root token or universal service-role key.
- Put private objects in private buckets and bypass shared CDN cache. Public CDN URLs must not grant upload/delete permissions.
- Rate-limit and bound uploads, validate content and actual checksums, enforce tenant ownership server-side, and use durable deletion jobs.
- Keep off-host encrypted backups with separate credentials and test restore into an isolated environment.
- Monitor both backends separately; alerts must still be delivered if one backend is down.

## Acceptance tests

1. Stop Testagram API: existing Backend A login, feed/search, posts and legacy media still work; only explicitly Testagram-owned capabilities degrade.
2. Stop Backend A: Testagram-owned health, uploads, completion, private retrieval and deletion work when its own dependencies are healthy.
3. Stop Redis/Upstash: durable ownership and quota correctness remain; only cache/rate-limit behavior degrades according to policy.
4. Stop one object store: requests fail clearly for its objects; no success-shaped response or unverified fallback occurs.
5. Revoke Backend A credentials: Backend B remains isolated and cannot use them; repeat in reverse.
6. Attempt cross-account media access, wrong audience token, replayed upload, oversized upload and private-cache access: all are denied.
7. Restore each backend from its own off-host backup and verify its own data independently.
8. Demonstrate alerts for API down, DB unavailable, object-store errors, queue age, disk pressure and backup failure.

## Rollout

Preserve existing routes and data. Introduce routing/configuration behind explicit feature flags. Test in staging with each backend stopped independently. Enable by capability gradually. Do not advertise automatic disaster recovery until replication, RPO/RTO, reconciliation and restore drills are demonstrated.
