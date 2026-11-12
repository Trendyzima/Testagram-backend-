# Legacy identity reconciliation (offline only)

## Current authentication architecture

Appwrite is the interactive identity provider. The Backend verifies Appwrite
sessions and uses the canonical account UUID stored in the independent
PostgreSQL database. There is no live Supabase identity-link endpoint.

The historical `POST /api/identity/link` flow is retired. The Backend does not
accept a legacy Supabase access token, does not call Supabase Auth to resolve
users, and does not link accounts by matching email addresses. Do not build a
client against that retired route.

## Existing-account migration policy

Accounts and related data that have not yet been reconciled must be migrated
through a controlled offline process. The process must:

1. Back up the source and destination databases and verify that the backups can
   be restored before making changes.
2. Establish an explicit, auditable mapping between the legacy account and the
   canonical Testagram account. Email equality alone is not proof of identity.
3. Preserve the canonical user UUID where the migration plan requires it and
   detect collisions before writing.
4. Reconcile dependent records independently: profiles, posts, replies,
   follows, blocks, messages, media ownership, wallet records, recovery data,
   and federation actor references must not be assumed migrated because the
   account row exists.
5. Run row-count, uniqueness, referential-integrity and sampled content
   verification, then produce a signed migration report.
6. Keep the original data read-only until acceptance and rollback windows have
   expired.

No automated account linking or destructive legacy-data cleanup is authorized
by this document.

## Runtime configuration

The Backend requires its Appwrite endpoint/project and canonical database
connection. It does not require `SUPABASE_URL`, `SUPABASE_PUBLISHABLE_KEY`,
or `SUPABASE_SERVICE_ROLE_KEY` for identity verification. Never place provider
secrets in Flutter/browser configuration or commit real credentials.

## Existing self-hosted database warning

Numbered SQL mounts in self-hosted Compose files are PostgreSQL
first-initialization scripts. PostgreSQL does not rerun them when a persistent
data volume already contains a database. Existing installations must apply
reviewed additive migrations through the backup-first deployment procedure.
Changing the Compose file alone does not upgrade an existing volume. Do not
delete the volume to force initialization.

## Release rule

The identity migration is complete only after the user mapping and every
required dependent data domain have been reconciled and independently verified.
Until then, keep legacy data intact and retain a tested rollback path.
