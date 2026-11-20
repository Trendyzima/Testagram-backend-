# Appwrite runtime retirement

Appwrite is not part of the supported Testagram architecture. The canonical
Backend is the only user/staff identity and session authority.

The retirement is deliberately split into two safety boundaries:

1. Runtime source: the unused Appwrite HTTP database client and the one-off
   Appwrite-to-native migration runner are removed. The canonical `cmd/api`
   runtime contains no Appwrite integration.
2. Existing database records: historical identity mapping and consent rows
   are retained for audit/reconciliation history, but the retirement migration
   revokes schema, table and sequence access from PUBLIC, `testagram_app` and
   `service_role`, including default privileges for future objects.

The old dated Appwrite migrations remain immutable migration history. They must
not be deleted or rewritten: environments that already applied them need a
consistent migration ledger. They do not authorize a live Appwrite connection
or authentication fallback.

Apply `migrations/20261122_retire_appwrite_identity_runtime.sql` through the
normal reviewed PostgreSQL migration process. Do not run it manually against
production without the approved backup, migration window and rollback plan.
The migration preserves rows; it does not drop the legacy schema or user data.
