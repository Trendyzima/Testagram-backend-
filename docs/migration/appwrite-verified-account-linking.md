# Verified Appwrite account linking

This is a deliberately narrow bridge for existing Testagram users. It is not a
global authentication cutover and does not migrate profile or social data.

## Proof requirements

The browser must hold both a current Appwrite session and a current legacy
Supabase access token from the existing signed-in session. The frontend sends
the Appwrite JWT as the Authorization bearer and the legacy token as the
X-Testagram-Legacy-Authorization bearer to POST /api/identity/link. The linking
method does not persist either token.

The Go API independently verifies both tokens with their respective identity
providers. It requires a verified Appwrite email and a valid legacy Supabase
user UUID. Email equality is intentionally not used as proof of ownership.

## Data safety rules

- The legacy UUID must already exist in testagram_local.accounts.
- The canonical account UUID must remain exactly the legacy UUID.
- The mapping is unique by Appwrite user ID, canonical user ID and legacy user
  ID. Existing conflicting mappings return HTTP 409.
- Repeating the same valid link is idempotent.
- Unknown/unmigrated accounts are rejected; this endpoint never creates a
  profile, imports data, or silently maps by email.
- The database transaction serializes account linking and commits the mapping
  atomically. Attempts are rate-limited per verified Appwrite identity.
- Successful links and idempotent relinks are written to an audit table in the
  same transaction as the mapping.
- This endpoint does not change TESTAGRAM_AUTH_PROVIDER, the frontend
  AuthProvider, existing sessions, or any production account.

## Prerequisites

Apply the independent-auth migration
20261019_testagram_independent_auth.sql, local account migration
20261019_testagram_local_accounts_posts.sql, Appwrite identity bridge
20261106_appwrite_identity_bridge.sql, and audit migration
20261106_appwrite_identity_link_audit.sql to the independent PostgreSQL database
through the reviewed deployment migration process. Configure server-only
APPWRITE_ENDPOINT, APPWRITE_PROJECT_ID, SUPABASE_URL, and
SUPABASE_PUBLISHABLE_KEY.

Do not expose server credentials in VITE_* variables. The frontend requires
VITE_TESTAGRAM_API_URL to point to the independent Go API origin.

## Existing self-hosted database warning

The numbered SQL mounts in the self-hosted Compose file are PostgreSQL
first-initialization scripts. PostgreSQL does not rerun them when the persistent
data volume already contains a database. Existing installations must apply the
new additive migration through the documented, backup-first migration
procedure; changing the Compose file alone does not upgrade an existing volume.
Do not delete the volume to force initialization.

## Deliberate limitations

This first bridge only links an account whose canonical row has already been
migrated with the same UUID. Accounts without a canonical row must go through a
separate, audited data migration/reconciliation process first. Account linking
is not proof that posts, follows, messages, media ownership, recovery workflows,
or federation actor keys have all been migrated.

Do not remove the legacy Supabase session or switch the global app session
until a separate migration release verifies all required data domains and
provides a tested rollback.
