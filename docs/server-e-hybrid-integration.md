# XClone + Testagram backend: hybrid Server E integration

## Goal

Connect the XClone client repository
([Trendyzima/XClone-9b4dz1-9b4dz1-8936-20260809tr](https://github.com/Trendyzima/XClone-9b4dz1-9b4dz1-8936-20260809tr))
to this Testagram application backend without disrupting the existing Server E services:
Cloudflare, Firebase, Redis, and Upstash.

This is an integration contract, not permission to change production DNS, secrets, or routing. Roll out with a staging origin first and keep a rollback path.

## Architecture and responsibility boundaries

| Component | Responsibility during hybrid rollout | Do not do |
| --- | --- | --- |
| XClone web/mobile client | UI, session token acquisition, capability/media API calls | Put service-role keys, Redis passwords, Upstash tokens, or Firebase server credentials in `VITE_*` variables |
| Testagram backend API | Authenticated capability gateway, bounded media lifecycle, health/readiness, metrics | Treat the gateway as complete replacement for every Supabase RPC or product service |
| Supabase/Auth currently used by XClone | Continue authenticating existing users and enforcing RLS while the backend validates/forwards their JWT | Switch to a different project or assume JWTs from different projects are interchangeable |
| Cloudflare | Keep existing DNS, proxy, TLS, edge security and media hostname configuration; add a dedicated API hostname/rule only after origin smoke tests | Purge/change existing production records or cache authenticated API responses as part of a code rollout |
| Firebase | Keep current configured client integrations and credentials unchanged until each Firebase responsibility has an audited replacement | Remove Firebase SDK/config or move its data/auth/push responsibilities by assumption |
| Redis / local Valkey | Self-hosted backend platform's internal cache/rate-limit/queue dependency | Expose the internal Redis-compatible service publicly or place its credentials in the browser |
| Upstash | Keep existing Upstash consumers and credentials running while their usage is inventoried and migrated deliberately, if desired | Replace Upstash endpoints with the private Valkey hostname without changing the consumer/network contract |
| CDN/object storage | Existing media host remains the public delivery path; backend authorizes uploads, completion, ownership, and private downloads | Return private objects through public caching or enable virtual storage before tenant-path and E2E gates pass |

The backend repository owns the application/API and optional self-hosted platform. The data-centre repository owns VPS assembly, edge configuration and host operations. XClone owns browser/mobile integration. Keep these responsibilities linked through versioned contracts rather than copying one repository into another.

## Client configuration and staged routing

### Phase A — capability gateway only

Set this in the **XClone frontend build environment** for staging:

```dotenv
VITE_TESTAGRAM_API_URL=https://api-staging.testagram.site
```

The client constructs `https://api-staging.testagram.site/api/capability`. Keep the existing `VITE_SUPABASE_URL`, publishable key, Firebase settings, Redis/Upstash service configuration, and other provider integrations unchanged for this phase.

The gateway forwards the user's existing Bearer JWT and the public Supabase publishable key to the configured Supabase capability RPC. It must not use a service-role key to bypass row-level security. This is a controlled API gateway migration, not a claim that all product operations have been moved off Supabase.

The API origin must use HTTPS, allow the actual Testagram web origin in `ALLOWED_ORIGINS`, and be routed to the backend through an operator-configured Cloudflare rule/tunnel/reverse proxy. Do not enable caching for authenticated POSTs.

Rollback: remove `VITE_TESTAGRAM_API_URL` from the frontend build environment and rebuild; the client returns to its existing same-origin `/api/capability` route. Do not change Auth project settings as part of this rollback.

### Phase B — media API

The backend media contract is:

| Method + path | Purpose |
| --- | --- |
| `POST /api/media/upload-intent` | Create a bounded, owner-scoped upload intent |
| `OPTIONS /api/media/upload` | CORS preflight for browser uploads |
| `PUT /api/media/upload?key=...` | Stream bytes through the bounded API upload gateway |
| `POST /api/media/complete` | Verify size/checksum and complete the asset lifecycle |
| `POST /api/media/attach` | Attach an owned asset to an authorized post |
| `POST /api/media/list` | List the caller's media |
| `POST /api/media/private-download-url` | Authorize short-lived private download |
| `POST /api/media/delete` | Delete an owned asset |

The client compatibility proxy in XClone's `api/media.ts` uses server-only `TESTAGRAM_MEDIA_BACKEND_ORIGIN` (or `TESTAGRAM_BACKEND_ORIGIN`) to reach this API. Configure those variables only in the server deployment environment, never in browser-visible `VITE_*` settings. Keep the existing media CDN base URL for public delivery.

The open backend PR [#73 — Align self-hosted media uploads with XClone](https://github.com/Trendyzima/Testagram-backend-/pull/73) fixes a confirmed upload preflight and legacy/virtual upload path mismatch. Do not declare the media cutover complete until that PR's CI passes and staging verifies intent → PUT → completion → attachment → private retrieval → deletion with real authenticated test accounts.

Keep `VIRTUAL_STORAGE_ENABLED=false` until the storage provider, tenant-scoped keys, migrations and end-to-end lifecycle tests have passed. A code flag or a green compile is not a runtime proof.

### Phase C — migrate further capabilities individually

For each feature family (search, feeds, notifications, federation, messaging, communities, billing, creator tools), record:
1. the client entry point and current provider;
2. the backend endpoint/RPC and auth/RLS boundary;
3. the response/error schema;
4. timeout, rate-limit and retry behavior;
5. a regression test and staging smoke test;
6. a rollback switch.

Do not point every client request at the new API until the backend actually implements or safely proxies that operation. Unknown routes must fail clearly rather than silently falling back to another identity or data store.

## Deployment and smoke-test gates

1. Deploy the backend to a staging origin with existing provider credentials unchanged.
2. Configure TLS and a Cloudflare route for the staging API hostname. Keep the origin private where possible; verify origin TLS rather than disabling certificate checks.
3. Confirm `GET /healthz` succeeds and `GET /readyz` succeeds only when its database dependency is available.
4. Scrape `GET /metrics` only from the private monitoring network; do not expose it publicly.
5. From an authorized staging client, test sign-in with the existing Auth project, then one capability read and one capability mutation. Confirm the backend forwards the user's JWT and RLS still rejects unauthorized access.
6. Test upload intent, bounded PUT, checksum completion, attach, private download authorization, and delete. Confirm public URLs use the existing CDN hostname.
7. Check Cloudflare edge/origin logs, API request IDs, backend logs, Prometheus/Grafana and alert delivery for the same test window.
8. Run XClone typecheck/lint/build and backend race tests/vet/build/Compose validation. Green CI is necessary but does not replace staging.
9. Roll out the API URL to a small staging cohort first. Keep the old same-origin route available until metrics show stable latency and error rates.
10. Document the rollback owner and remove staging credentials only after the migration is accepted.

## Monitoring

Use the existing private operations stack:
- Staff console: `ADMIN_DOMAIN` (default `ops.testagram.site`), after staff roles and Auth redirects are configured.
- Grafana: private/loopback-bound; access through an SSH tunnel.
- Prometheus: API request/error/latency metrics, database pool pressure, platform cache/rate-limit counters and configured scrape targets.
- Alertmanager: alerts are evaluated, but notification delivery must be configured and tested before treating it as on-call monitoring.

Do not publish Grafana, Prometheus, internal platform, Redis, or control-plane ports just to make a dashboard convenient.

## Acceptance checklist

- [ ] Existing Cloudflare DNS/proxy, Firebase integrations, Redis consumers and Upstash consumers still work unchanged.
- [ ] Staging API TLS, origin routing, CORS and health/readiness checks pass.
- [ ] Existing Auth session works through the API gateway; no service-role bypass.
- [ ] Capability read/write tests pass with expected authorization failures for unauthorized users.
- [ ] Media lifecycle passes from intent through deletion; private media never becomes publicly cacheable.
- [ ] XClone and backend CI pass on the exact reviewed commits.
- [ ] Dashboard metrics and alert notifications are observed during a staging smoke run.
- [ ] Rollback to the existing same-origin API route is tested.
- [ ] `VIRTUAL_STORAGE_ENABLED=false` remains in force until its separate release gate passes.

**Current status:** documentation of the intended integration boundary only. This document does not change production DNS, provider configuration, secrets, or traffic routing.
