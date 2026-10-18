# Testagram Virtual Storage — Foundation

## What this service is

Virtual Storage is a logical storage control plane, not a new object-storage engine. Application services address logical namespaces such as `post-media`, `private-media`, `avatars`, `exports`, and `backups`; a provider adapter maps each namespace to a physical bucket or storage account. The API never exposes provider credentials or requires a browser to know the physical bucket name.

The policy layer and a standard-library S3-compatible SigV4 provider now exist in `internal/virtualstorage`. When `VIRTUAL_STORAGE_ENABLED=true`, the API uses tenant-scoped physical keys for new uploads, verifies provider-observed metadata at completion, and routes private retrieval and deletion through the namespace manager. The flag defaults to `false`; legacy rows and URLs remain unchanged. This is an opt-in integration, not proof that the production CDN/origin path mapping or live R2/MinIO lifecycle has passed.

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
          +---- S3-compatible SigV4 provider (implemented; opt-in)
          +---- local development adapter (not yet implemented)
          v
Private/public physical buckets and CDN
```

Large uploads and downloads should use short-lived signed URLs so application servers do not proxy file bytes. Private objects must not be published through a public CDN. Public visibility is read-only to anonymous users; it never grants upload or delete permissions.

## Security invariants

- Tenant identity comes from verified server-side authentication, never a caller-supplied owner ID.
- Keys are relative, normalized, bounded, and reject traversal, absolute paths, NUL and control characters.
- Logical namespace names are allow-listed at startup.
- Size and content-type policies are checked before a signed upload URL is issued. Completion HEADs the object and verifies size/type against the persisted intent. The signed PUT URL itself does not enforce a maximum byte count; checksum computation/verification is still pending.
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

Do not map public and private logical namespaces to the same physical bucket unless the provider policy can enforce equivalent isolation. The existing media signer remains the default production path while `VIRTUAL_STORAGE_ENABLED=false`. New virtual objects require the public CDN/origin to serve `tenants/{owner}/{logical-key}` physical paths.

## Release gates before production use

1. Verify the S3 provider's SigV4 compatibility with MinIO and R2, including signed PUT/GET, HEAD, DELETE, and paginated LIST; retries are currently supplied by the durable deletion worker rather than a general provider retry layer.
2. Add durable object metadata, ownership, lifecycle state, quotas, idempotency, and a deletion/outbox queue in PostgreSQL.
3. Add authenticated HTTP routes and derive tenant identity from verified JWT claims.
4. Verify content length and actual content type after upload; checksum computation and malware scanning still need implementation. Do not trust client claims.
5. Run integration tests against an S3-compatible test service, then real R2: signed PUT, HEAD, completion, public CDN fetch, private download, cross-tenant denial, expired URL, retry/delete, orphan cleanup and quota concurrency.
6. Configure separate public/private buckets, CORS, CDN origin access, lifecycle rules, backups and alerting.
7. Keep the feature disabled until the complete release gate is green.

## Current status

The policy package, S3-compatible provider, unit tests, opt-in API wiring, and namespace-aware cleanup paths are implemented on the feature branch. Live provider tests, CDN path confirmation, checksum verification, and complete release-gate validation remain outstanding.
