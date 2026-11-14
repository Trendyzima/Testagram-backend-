# Deployment policy: Instabox VPS only

**Status: mandatory architecture decision. Render is retired and prohibited.**

The Instabox VPS, operated through the Testagram Data Centre deployment/control plane, is the sole authoritative self-hosted deployment target for the Backend and the seven-repository Testagram runtime.

## Non-negotiable rules

1. Do not add Render services, blueprints, manifests, configuration, deployment workflows, secrets, URLs, badges, or infrastructure dependencies.
2. Do not restore `render.yaml`, `render.yml`, `.render/`, Render-specific Docker build configuration, or Render deployment steps.
3. Do not use Render as a fallback, preview, staging, failover, or secondary hosting provider.
4. Backend runtime changes must be deployed through the reviewed Instabox VPS Compose/control-plane path. Health/readiness, secrets, migrations, image digests, rollback and watchdog behavior must be verified there.
5. CI must run `python3 scripts/guard-no-render.py`. A violation fails the build.
6. Any future hosting change requires an explicit architecture decision recorded in the shared Testagram API contract and reviewed across all seven repositories. No PR may silently reintroduce a retired host.

## Runtime authority

- Instabox VPS / Data Centre: deployment, runtime operations, monitoring, backup and recovery.
- Backend `cmd/api`: public product API runtime.
- PostgreSQL: canonical product relational state.
- Appwrite: authentication/session provider and identity provisioning.
- Data Centre media service: private media lifecycle and provider orchestration, gated until staging acceptance.
- CDN: media delivery only.
- Flutter: public Backend API client.
- Algorithm and Federation: private authenticated services.

## Enforcement scope

The guard rejects Render deployment manifests/directories and Render provider URLs or deployment commands in tracked operational configuration (GitHub Actions, Docker/Compose, deployment scripts, infrastructure manifests and root deployment metadata). Historical decision records may mention Render to explain its retirement; they do not authorize deployment.
