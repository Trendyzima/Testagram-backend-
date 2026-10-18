# Testagram platform expansion: first-party app hosting and storage rentals

## Product goal

Keep Testagram itself as the first demanding tenant, then expose the same reliable backend as a paid/self-service platform for MVP builders. The product should let a customer create a workspace, deploy an app/API/worker, attach managed storage, use a database, bind a domain, observe usage, and export/restore their data without sharing another tenant's credentials or data.

This is a staged platform roadmap, not a claim that a public multi-tenant rental product is already launch-ready. Migration 009 establishes backend-only tenant, project, subscription, quota, resource, metering and billing-event records. Existing hosting APIs and media ownership are not automatically migrated into this model.

## Product surface

1. **Workspace / organization:** personal or team tenant, role-based membership (owner, admin, developer, billing, viewer), invitations, audit history.
2. **App hosting:** Git-based or image-based deployments, immutable image digests, isolated build jobs, environment/secret references, health checks, rollbacks, custom domains and logs.
3. **Managed storage:** logical buckets/namespaces, private/public access policies, resumable uploads, object lifecycle, quotas, signed URLs, egress metering, export and deletion.
4. **Backend-as-a-service:** managed PostgreSQL databases, backups/restore, migrations, secrets, API gateway and authentication. Keep each customer's database credentials isolated; never expose platform superuser credentials.
5. **Platform operations:** per-tenant resource limits, abuse controls, deployment queue, usage dashboards, alerting, support audit, maintenance windows and tenant suspension.
6. **Billing:** plans and entitlements, a durable usage ledger, verified/idempotent payment webhooks, invoices/credits and a graceful past-due lifecycle. No payment provider is assumed or activated by this migration.

## Multi-tenant schema in migration 009

- `tenants` and `tenant_members`: ownership and role membership.
- `plans` and `subscriptions`: commercial status and machine-readable entitlements/limits.
- `projects` and `resources`: project-scoped services and desired resource allocations.
- `quota_counters` and `resource_reservations`: transactional capacity reservation to stop concurrent requests from oversubscribing quotas.
- `usage_events`: idempotent usage facts; never use mutable aggregate counters as the sole billing source.
- `billing_events`: durable, idempotent webhook inbox after signature verification.
- `audit_events`: security-relevant tenant activity.

All tables enable row-level security with no client-facing policies. That intentionally denies direct client access by default. The backend must still scope every query by tenant and check membership/role; service-role access bypasses RLS and is not a substitute for application authorization.

## Hard isolation requirements before external customers

- Every route resolves tenant from a verified session and checks membership/role; never trust a tenant ID supplied in JSON as authorization.
- Add negative cross-tenant tests for every read, mutation, log, backup, domain, storage key, environment variable and billing record.
- Store secrets in a dedicated encrypted secret manager or encrypted-at-rest service; API responses and logs must never echo secret values.
- Build and run untrusted customer code in disposable, unprivileged sandboxes with CPU/memory/disk/PID/network limits. Never run customer images with the Docker socket, host networking, privileged mode, host mounts, or platform secrets.
- Separate build workers from runtime nodes and control-plane/database networks. Apply egress restrictions and rate limits.
- Use image digests, SBOMs, signature/provenance checks, vulnerability scanning and controlled base-image updates. Do not trust a mutable tag or an unverified third-party mirror in production.
- Apply transactional quotas before provisioning; reconcile reservations on failures, retries and deletion.
- Meter storage bytes, request count and egress at the provider/edge where possible. Usage events must have unique source event IDs to avoid duplicate charges.
- Implement data export, retention, deletion, tenant suspension and backup restore before taking customer data.
- Payment webhooks must have signature validation, replay protection, idempotency and a dead-letter path. A plan change must not silently delete customer data.
- Publish status/incident communication, acceptable-use rules, privacy terms, support process and a clear refund/data-retention policy before launch.

## Recommended rollout gates

### Gate A — Testagram dogfood
- Finish the virtual-storage PR and its real MinIO lifecycle check.
- Apply migrations 008 and 009 to a staging VPS with a verified database backup.
- Exercise upload, completion, checksum, private/public fetch, cross-owner denial, expiry, cancellation and deletion.
- Restore database metadata and object bytes into a clean environment; compare hashes.

### Gate B — single-tenant self-service
- Implement tenant creation/membership APIs and role middleware.
- Implement project/resource CRUD and a provisioning job state machine with idempotency keys.
- Implement quota reservation and release in transactions, plus tenant-scoped audit events.
- Add a usage dashboard and operator reconciliation command.

### Gate C — safe customer compute
- Use disposable sandboxed build/runtime workers with resource and network isolation.
- Implement deployment logs, health checks, immutable release artifacts, rollback and per-tenant limits.
- Pen-test cross-tenant access and run resource-exhaustion tests before exposing public signup.

### Gate D — paid MVP
- Select a payment provider based on the target market and settlement support; configure signed webhooks, invoices and a support/refund flow.
- Start with explicit limits and a small allow-list of runtimes/regions.
- Add billing previews, overage policy, abuse detection, suspension grace period and export-before-deletion.
- Launch an invite-only beta, observe real costs and failure rates, then expand.

## Operating model and pricing

Do not promise "unlimited" storage, CPU or bandwidth. Set plan limits based on measured VPS capacity and include headroom for backups, replication, operating system, PostgreSQL, caches, logs and emergency recovery. Storage pricing must account for both GB-month and egress/request costs; compute pricing should be based on reserved CPU/memory and uptime; databases/backup retention should have separate limits. Start with a free/internal tier and transparent fixed limits, then price only after a week or more of representative workload measurements.

For resilience, separate control-plane metadata from customer object bytes, keep off-site encrypted backups, use immutable image references, and test restores regularly. Multiple storage nodes alone do not make a reliable storage service; placement, checksums, replication, repair, failure-domain awareness and restore tests are prerequisites.

## Current boundaries

- Migration 009 creates tables only; it does not provision tenants, implement tenant APIs, process payments, deploy arbitrary customer code, or alter existing media ownership.
- The S3-compatible virtual-storage path remains opt-in until CDN routing, checksum/size enforcement and full live lifecycle checks pass.
- The deployment's MinIO image source needs provenance verification and digest pinning before production use.
- No public rental signup or customer workloads should be enabled based solely on schema creation.
