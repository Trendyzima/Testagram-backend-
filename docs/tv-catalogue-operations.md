# TV catalogue provisioning and health monitoring

## Ownership

- `testagram_tv.channel_catalog` is the authoritative channel definition table.
- `testagram_tv.channel_health` stores mutable probe observations only.
- The GitHub Actions TV monitor can read the catalogue through the OIDC-protected internal endpoint. Its database role has SELECT-only access to the catalogue and cannot create or edit channel definitions.
- Health writes are accepted only when the channel is enabled and the submitted URL exactly matches the provisioned URL. Unknown, disabled, or stale catalogue entries return HTTP 409; duplicate IDs in a batch return HTTP 400.

## Migration

Apply `migrations/20261114_tv_channel_catalog.sql` using the normal guarded Backend migration process. It creates the catalogue and backfills existing channel definitions from the legacy health table once. It does not invent or fabricate channels.

## Provisioning

Provision verified channel entries using a privileged database operator/migration role after validating ownership, licensing/redistribution rights, and that each stream URL is a public HTTP(S) endpoint. Example:

```sql
INSERT INTO testagram_tv.channel_catalog
  (channel_id, stream_url, source, country, group_name, priority, enabled)
VALUES
  ('verified-channel-id', 'https://stream.example.invalid/live.m3u8',
   'verified-provider', 'KE', 'News', 100, true)
ON CONFLICT (channel_id) DO UPDATE SET
  stream_url = EXCLUDED.stream_url,
  source = EXCLUDED.source,
  country = EXCLUDED.country,
  group_name = EXCLUDED.group_name,
  priority = EXCLUDED.priority,
  enabled = EXCLUDED.enabled,
  updated_at = now();
```

The example hostname is deliberately non-operational; replace it only with a verified, authorized stream URL. Do not paste provider credentials or private stream URLs into source control.

To disable a channel without deleting its history:

```sql
UPDATE testagram_tv.channel_catalog
SET enabled = false, updated_at = now()
WHERE channel_id = 'verified-channel-id';
```

## Acceptance

1. Apply the migration and confirm the catalogue backfill count.
2. Provision verified entries through the privileged operator path.
3. Confirm the OIDC monitor catalogue response returns only enabled entries.
4. Confirm an unauthenticated catalogue request returns 401.
5. Confirm an unknown/disabled channel health write returns 409 and does not create a health row.
6. Run the scheduled monitor and verify persisted rows in `channel_health`.
7. Never treat an empty catalogue as successful production coverage; an empty catalogue means provisioning is incomplete.
