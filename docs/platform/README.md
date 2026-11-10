# Testagram Platform Documentation

This repository owns the canonical product API, PostgreSQL schemas/migrations,
media authorization and hosting control-plane API.

The Data Centre repository owns the internal platform API, video/notification
services, storage nodes and platform operations. Its internal platform
contract is documented in
[Data Centre OpenAPI](https://github.com/Trendyzima/Testagram-data-centre-/blob/main/docs/platform/openapi/internal-platform.yaml).

See the [five-repository integration contract](../five-repository-integration.md),
[hosting control-plane API](openapi/hosting-control-plane.yaml), and
[virtual storage foundation](virtual-storage.md).

Supabase/PostgREST/Edge Functions and Redis/Valkey/Upstash are not retained
runtime dependencies.
