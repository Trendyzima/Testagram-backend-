# Verified upstream integration policy

This document records upstreams that are maintained by the official Supabase organization and defines how Testagram should reuse them without blindly copying unrelated code into its Go backend.

## Upstream candidates

| Upstream | Intended use in Testagram | Integration boundary |
| --- | --- | --- |
| https://github.com/supabase/supabase | Canonical self-hosted Compose reference, service wiring, configuration and migration conventions | Pin a reviewed release/tag or commit. Compare its service topology with `deploy/self-hosted/`; do not overwrite Testagram's Compose file wholesale or replace its product-specific services. |
| https://github.com/supabase/cli | Local development, migration management, type generation and Edge Function workflows | Use a pinned CLI release in developer/CI workflows. Do not embed the CLI as a runtime dependency of the Go API. |
| https://github.com/supabase/storage | Reference implementation for object storage authorization, metadata, upload semantics and S3 compatibility | Integrate through documented APIs or adopt the official service as a separately versioned container. Do not copy individual source files into the custom media signer. |
| https://github.com/supabase/edge-runtime | Runtime for Deno-based Supabase Edge Functions | Run as a separately versioned service only where existing Edge Functions require it; keep secrets and function verification settings explicit. |
| https://github.com/supabase/auth | Authentication server source and compatibility reference | Prefer a pinned official image/release and the documented JWT/JWKS contract. Do not fork or replace authentication implementation without a dedicated migration and security review. |
| https://github.com/supabase/supabase-js | Official JavaScript client for web/mobile integrations | Consume as a versioned package in the product client, not by copying SDK source into this Go repository. |

The repositories above are official organization-owned upstreams; that is provenance verification, not a claim that every commit, release, or third-party contribution is vulnerability-free. Before adoption, review the exact release, license, security advisories, supported configuration, and compatibility with Testagram's existing schema and auth model.

## Integration rules

1. Prefer supported APIs and pinned container/package versions over source copying.
2. Never vendor an entire Supabase monorepo into this backend. It duplicates ownership, makes upgrades difficult, and risks silently changing authentication, schema, network exposure, or storage policy.
3. Keep Supabase Auth/Postgres/RLS as the source of truth until an explicit migration plan proves parity. The existing local PostgreSQL stack is not, by itself, a replacement for a deployed Supabase schema or its auth/storage services.
4. Pin upstreams to immutable release tags or commit SHAs for production. Avoid floating `latest` images.
5. Add contract tests for every adopted API and test upgrade candidates in an isolated environment before promotion.
6. Record the selected version, license, configuration delta, migration/rollback procedure, and owner for each integration.

## Immediate validation gates

- CI tests both Go modules independently, including race tests, vet and builds.
- CI scans both Go modules with govulncheck.
- Compose configuration is validated against the actual self-hosted Compose file.
- These checks do not replace integration tests against a disposable PostgreSQL/Valkey/object-storage stack, a real backup restore, or a production authorization review.
