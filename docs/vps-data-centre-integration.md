# VPS data-centre integration contract

This repository is the canonical application/control-plane source for Testagram. The companion [Testagram data-centre repository](https://github.com/Trendyzima/Testagram-data-centre-) owns the VPS assembly and upstream Supabase source inventory. Keep the boundary explicit: do not fork or patch official Supabase components here; consume their pinned builds through the data-centre deployment layer.

## Cross-repository work in review

The data-centre changes are deliberately separate draft PRs until their own CI and runtime gates pass:

- [#5 — Node-agent workload execution hardening](https://github.com/Trendyzima/Testagram-data-centre-/pull/5): immutable image references, explicit allowlist, safer volume resolution, bounded execution and cleanup work.
- [#6 — Edge request limits and immutable Supabase source pins](https://github.com/Trendyzima/Testagram-data-centre-/pull/6): route-specific Caddy body limits and an upstream lock inventory.
- [#7 — Encrypted off-host backup and isolated restore drill](https://github.com/Trendyzima/Testagram-data-centre-/pull/7): operator-run backup/restore scripts; not yet a successful production restore or an RPO/RTO claim.
- [#8 — Video upload quota and disk safeguards](https://github.com/Trendyzima/Testagram-data-centre-/pull/8): bounded per-instance upload concurrency, per-account video quota reservations and free-disk reserve checks.

Those PRs are not deployed by linking them from this repository. Merge/deploy remains blocked until CI passes and the operator validates the target host configuration.

## Workload policy contract

The control plane now requires all three conditions before it can assign a workload:

1. The request names an immutable `sha256:` image digest.
2. The full `image@sha256:...` reference is an exact entry in `VPS_ALLOWED_WORKLOAD_IMAGES`.
3. The candidate node advertises `capabilities.workloadExecution=true`, which the node agent should only advertise when it has the same non-empty allowlist configured.

Configure the **same comma-separated exact image references** in the control-plane and every eligible node-agent environment. Keep the setting empty until the node agent version that enforces this policy is deployed. An empty setting intentionally prevents new workload scheduling. Do not add mutable tags, wildcards, or user-supplied image references.

Example (replace with actual, reviewed digests; these are illustrative only):

```dotenv
VPS_ALLOWED_WORKLOAD_IMAGES=ghcr.io/your-org/approved-worker@sha256:REPLACE_WITH_64_HEX_DIGEST
```

The control-plane and node-agent must be upgraded as a coordinated pair. If the control plane is upgraded first, old nodes that do not advertise the capability will be skipped. This fail-closed behavior may pause workload scheduling, but it avoids assigning jobs to agents that have not asserted the execution policy.

## Release gates

Before enabling workloads or virtual storage on a real VPS:

- Run Go race tests, vet/build, Compose validation and the full CI workflow for both repositories.
- Confirm node-agent volume paths cannot escape the approved root through symlinks, and verify timeout/cancellation cleanup against both Docker and Podman.
- Verify workload network isolation, PID/disk limits, command authorization, aggregate resource reservations and orphan-container cleanup under forced termination.
- Test upload quota reservation under concurrent requests; exercise upload cancellation, disk pressure, oversized and chunked bodies, and confirm expired reservations are reclaimed.
- Run an encrypted backup to a genuinely off-host destination; restore the exact artifact into an isolated environment and test login, upload, private-media retrieval and playback. Record elapsed restore time and observed RPO/RTO.
- Confirm pinned Supabase revisions build and pass a full staged-stack smoke test before promoting those exact artifacts.
- Keep `VIRTUAL_STORAGE_ENABLED=false` until tenant-prefixed object paths and end-to-end tests pass.

## Responsibility split

| Area | Source of truth |
| --- | --- |
| Application API, media metadata/quota claims, platform service and hosting control plane | This repository |
| VPS assembly, node-agent packaging/operations, Supabase upstream source lock, edge configuration and host backup/restore procedures | [Testagram data-centre](https://github.com/Trendyzima/Testagram-data-centre-) |
| Browser/mobile behavior and opt-in API routing | Testagram client repositories |
| Auth, database, realtime and object-storage engines | Official Supabase upstream revisions, pinned by the data-centre build |

Do not copy the whole data-centre repository into this one. Integrate through explicit environment contracts, API/schema compatibility, shared CI release gates, and immutable build artifacts so security fixes do not drift between duplicate implementations.
