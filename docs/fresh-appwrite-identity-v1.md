# Fresh Appwrite identity lifecycle (v1)

## Authority boundaries

- Appwrite Auth authenticates credentials and issues browser sessions.
- The Backend verifies every Appwrite JWT by calling the configured Appwrite Account endpoint. It never decodes the JWT or trusts a client-supplied user ID.
- PostgreSQL owns the canonical Testagram UUID, username, display name, account status and all social/application records.
- An Appwrite account is not a Testagram account until a canonical mapping exists.

## API

- `GET /api/identity/session`: verifies the Appwrite session and resolves the existing mapping plus active canonical account.
- `POST /api/identity/provision`: for a verified Appwrite email, creates a new canonical account and mapping atomically, but only after a trusted KYC approval record and the complete legacy email inventory gate exist.
- `POST /internal/identity/approval`: accepts an attestation only from the configured identity-verification worker using the server-only `APPWRITE_IDENTITY_INGEST_TOKEN`. It is idempotent for the same provider event and rejects conflicting approvals.
- `POST /api/identity/link`: existing account path; requires simultaneous Appwrite and legacy Supabase session proof and preserves the legacy canonical UUID.

Provisioning is idempotent for an already-mapped Appwrite ID. A reserved legacy email returns 409 and must use the verified linking path. New accounts are also rejected unless the identity-verification worker has recorded an approval for that exact Appwrite user ID; a browser cannot mark itself approved. The same email is never sufficient proof to claim an existing account.

## Migration safety gate

Apply `20261107_appwrite_identity_lifecycle.sql`. The singleton inventory flag defaults to false. New account provisioning returns 503 until operators import and reconcile every legacy email/UUID into `testagram_identity.legacy_email_reservations` and explicitly set `legacy_inventory_complete=true`. Do not set that flag based on an estimate or partial export. The KYC approval table is separate: only the trusted verification worker may populate it after the existing identity-verification engine has approved the registration.

The importer/reconciliation job is a separate controlled operation: export the legacy user inventory from the old identity provider, reconcile it with canonical account rows and dependent data, import only normalized email plus stable legacy UUID, compare counts/checksums, and record the source export hash and operator approval. Do not put raw passwords, access tokens or recovery secrets in this inventory.

## Data preservation

No existing account, post, follow, message, media object or canonical UUID is deleted or rewritten by these endpoints. Existing accounts link only after both sessions are independently verified. Accounts not yet present in the canonical database remain blocked pending a separately reviewed data import; the API does not silently create replacements.

## Deployment

Configure server-side `APPWRITE_ENDPOINT`, `APPWRITE_PROJECT_ID`, and a high-entropy `APPWRITE_IDENTITY_INGEST_TOKEN` shared only with the trusted KYC worker. Set `APPWRITE_KYC_BINDING_ENABLED=true` only after the XClone identity-signup Edge Function is deployed with the same flag, Appwrite project settings, Backend URL and ingest token, and the approval callback has passed an end-to-end staging test. The Backend rejects approval ingestion and new canonical provisioning while this flag is false. Never expose an Appwrite API key in browser variables. Keep the production auth provider on the existing provider until XClone's global session provider, legal acceptance, recovery, direct Supabase dependencies and all staging flows have been migrated. This PR adds the identity API; it does not switch production traffic.
