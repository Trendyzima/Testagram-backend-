# Testagram Virtual Storage — Foundation

## What this service is

Virtual Storage is a logical storage control plane, not a new object-storage engine. Application services address logical namespaces such as `post-media`, `private-media`, `avatars`, `exports`, and `backups`; a provider adapter maps each namespace to a physical bucket or storage account. The API never exposes provider credentials or requires a browser to know the physical bucket name.

This first increment adds the provider-neutral policy layer in `internal/virtualstorage`. It validates logical namespace configuration, scopes object keys by tenant, checks MIME and object-size policy, limits signed URL lifetimes, and blocks cross-tenant private reads and deletes. It does **not** yet configure a production provider, create buckets, persist object metadata/quotas, or expose new public HTTP routes.

## Architecture

```text
Testagram web/mobile clients
          | authenticated API request
          v
Testagram API / storage policy
          | tenant + namespace + size/type checks
          v
Virtual Storage Manager
          | Provider interface
          +---- R2 / S3-compatible adapter (planned integration)
          +---- local development adapter (planned)
          v
Private/public physical buckets and CDN
```

Large uploads and downloads should use short-lived signed URLs so application servers do not proxy file bytes. Private objects must not be published through a public CDN. Public visibility is read-only to anonymous users; it never grants upload or delete permissions.

## Security invariants

- Tenant identity comes from verified server-side authentication, never a caller-supplied owner ID.
- Keys are relative, normalized, bounded, and reject traversal, absolute paths, NUL and control characters.
- Logical namespace names are allow-listed at startup.
- Size and content-type policies are checked before a signed upload URL is issued. The completion path must still HEAD the object and verify actual size/type/checksum before marking it ready.
- Private reads require requester tenant == owner tenant, unless a separate explicit sharing policy authorizes access.
- Deletion is always owner-scoped and should be queued/retried when provider deletion fails.
- Signed URL TTL is bounded to 1–60 minutes.
- Provider credentials are server-only secrets; never put them in Vite/browser configuration or commit them to Git.
- Metadata and quota accounting need durable transactional storage; process-local counters are not acceptable for multi-instance production.

## Initial logical namespace policy

Recommended production mappings (physical names are deployment configuration, not API contracts):

| Logical namespace | Access | Suggested max object | Content types |
|---|---|---:|---|
| `avatars` | public read, owner write | 10 MiB | JPEG, PNG, WebP, AVIF |
| `post-media` | public/private according to post visibility | 25 MiB per image | JPEG, PNG, WebP, GIF, AVIF |
| `private-media` | owner or explicitly authorized share | 500 MiB | allow-listed image/video |
| `exports` | private | deployment-specific | application/octet-stream, archive formats |
| `backups` | operator-only | deployment-specific | application/octet-stream |

Do not map public and private logical namespaces to the same physical bucket unless the provider policy can enforce equivalent isolation. The existing media signer remains the current production media path until provider integration and live end-to-end tests pass.

## Release gates before production use

1. Implement and review an R2/S3 provider adapter with signed PUT/GET, HEAD, DELETE, paginated LIST, and bounded retries.
2. Add durable object metadata, ownership, lifecycle state, quotas, idempotency, and a deletion/outbox queue in PostgreSQL.
3. Add authenticated HTTP routes and derive tenant identity from verified JWT claims.
4. Verify content length, actual content type, checksum and malware/moderation state after upload; do not trust client claims.
5. Run integration tests against an S3-compatible test service, then real R2: signed PUT, HEAD, completion, public CDN fetch, private download, cross-tenant denial, expired URL, retry/delete, orphan cleanup and quota concurrency.
6. Configure separate public/private buckets, CORS, CDN origin access, lifecycle rules, backups and alerting.
7. Keep the feature disabled until the complete release gate is green.

## Current status

The policy package and unit tests are implemented in this change. This is a foundation, not a claim that R2 is connected or that production storage is operational.
