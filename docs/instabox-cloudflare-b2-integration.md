# Instabox VPS, Cloudflare R2 and Backblaze integration

## Canonical application database

The production canonical Testagram PostgreSQL database is hosted on the Instabox VPS under the Backend deployment. It owns native user accounts, password credentials, email-verification state, sessions, profiles, identity-review records, social graph, posts, privacy/moderation state, media metadata, quotas, analytics events and transactional outboxes.

Testagram Backend owns signup, login, password recovery, email verification, access/refresh credential issuance and revocation. Backend resolves and authorizes only canonical Testagram UUIDs from PostgreSQL. No external identity-provider session, project identifier or API key is part of the production authentication path. Never auto-link accounts by matching email.

PostgreSQL must not be publicly exposed. Backend and authorized Data Centre services use private network access with TLS where traffic crosses a host boundary. Keep encrypted off-host backups, WAL/PITR or equivalent recovery, monitored disk capacity and tested restore. Cloudflare DNS, R2 and analytics do not replace this database.

## Media durability contract

Target durable user-media placement is three independent verified copies:

1. Instabox VPS private object storage (the local S3-compatible MinIO/object-storage service).
2. Cloudflare R2 private bucket.
3. Backblaze B2 private bucket.

Backend remains the public authorization boundary and owns upload intents, object keys, owner/visibility, quotas, checksums, media metadata, replica ledger and deletion tombstones. Data Centre operates the VPS storage service and replication/repair workers. Backend must not mark an asset ready under the three-copy policy until each provider has passed read-back SHA-256, byte-length and content-type verification.

Provider credentials are separate and server-side only. A node, browser or Flutter build never receives master S3 keys. ETags are not treated as portable SHA-256. A missing/unhealthy provider means the asset remains replicating or degraded, with bounded retries, alerts and repair; it is not silently counted as a verified replica.

## Testagram CDN and Cloudflare boundary

Testagram's own Go CDN is the public byte-delivery service. Cloudflare provides authoritative DNS, DNSSEC/domain security, R2 object storage and aggregated Cloudflare analytics. Do not depend on Cloudflare CDN/proxy caching or a Cloudflare Worker as the public media delivery path.

media.testagram.site resolves through Cloudflare DNS to Testagram CDN edge ingress. CDN origins are private and can use Instabox object storage, R2 and B2 in a health-aware fallback order. CDN validates Backend-issued object/viewer/scope-bound delivery authorization and must not expose origin credentials. Private media responses must never be shared-cacheable across viewers.

## Analytics ownership

Cloudflare DNS/Web Analytics provide aggregated domain and web-performance telemetry. Product analytics events are validated by Backend, minimized, consent-aware and stored in VPS PostgreSQL. CDN delivery metrics flow to Data Centre monitoring. Do not send JWTs, passwords, email addresses, recovery secrets or raw private media URLs to analytics.

## Existing implementation versus target

The self-hosted Backend stack provides PostgreSQL and a local S3-compatible MinIO service. The Data Centre overlay shares the Backend-owned PostgreSQL and private networks. This is the base for Instabox-hosted canonical data.

The current media implementation includes native upload, PostgreSQL media metadata, replication outbox and lifecycle workers. A configured three-provider target is not proof of a verified third replica: the exact provider write/read-back/checksum/repair/delete behavior and CDN failover must pass end-to-end tests before the three-copy policy is declared complete.

The custom CDN's S3-origin authentication/failover across all three providers also requires implementation and tests.

## Release gates

- Native signup, email verification, login, refresh rotation, logout and staff WebAuthn/MFA tests against the canonical VPS PostgreSQL database.
- VPS PostgreSQL TLS/private networking, backups, WAL/PITR and isolated restore.
- Three-provider media write/read-back/checksum/repair/delete tests with isolated buckets.
- Backend replica ledger and degraded-state policy.
- CDN origin adapters and failover for Instabox, R2 and B2, including private-media cache isolation.
- Cloudflare DNS-only records to custom CDN, DNSSEC and zone export/rollback.
- Product analytics retention/consent and Cloudflare analytics data minimization.
- End-to-end staging tests before production DNS, provider or database cutover.

No production settings are changed by this document.
