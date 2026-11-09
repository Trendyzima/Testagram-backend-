# Retired: legacy identity linking

The runtime endpoint `POST /api/identity/link` has been removed. It required a live Supabase session and made Supabase a production authentication dependency, which is incompatible with the Appwrite-only identity architecture.

Existing-account continuity must be handled by a controlled, offline migration process before cutover:

1. Export the required legacy account and ownership mapping from the legacy system using an authorized, audited migration operator.
2. Reconcile canonical user IDs against the Neon account records and preserve the existing account ownership mapping.
3. Provision or verify the corresponding Appwrite identity out of band, using a separately verified ownership process.
4. Import the Appwrite-to-canonical-user mapping into the Backend identity table using a reviewed, idempotent migration.
5. Verify profile, post, follow, message, media ownership, recovery and federation actor mappings before enabling the migrated account.
6. Keep migration artifacts encrypted, access-controlled and time-limited; never place legacy tokens or provider keys in the browser or Backend runtime.

The Backend now accepts Appwrite JWTs only. Unknown or unmapped Appwrite identities fail closed; the API does not silently create or map accounts by matching email addresses.

The historical SQL migrations and this document may mention Supabase solely to explain the retired migration source. They are not runtime dependencies. Do not restore the old linking endpoint or add Supabase credentials to the deployment environment.
