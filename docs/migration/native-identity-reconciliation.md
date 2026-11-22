# Historical identity reconciliation and native cutover

## Current state

Testagram's production authentication authority is native PostgreSQL-backed identity and sessions. The historical identity-provider integration is not an allowed runtime path. The native app must not accept provider JWTs, provider cookies or provider session IDs.

## Preserve evidence, do not preserve dependency

Older applied migrations created provider-keyed account mappings, staff reviewer IDs, consent evidence and a one-time account reconciliation ledger. These records may be needed to prove ownership, explain prior decisions or reconcile accounts. They are not a reason to keep the old provider online.

Do not:
- enable a transitional auth-provider flag;
- call an external provider during login, refresh, logout, staff authorization or identity provisioning;
- match accounts by email alone;
- import passwords or claim password continuity;
- delete old mapping/audit rows before reconciliation, backup and retention review;
- edit an already-applied migration.

## One-time legacy account import

The archived SQL importer is an operator-only historical migration aid. It requires a verified account mapping, a database backup, an operator identity and an evidence/change reference. It preserves canonical Testagram UUIDs and forces a password reset because external password hashes are not imported. It is not invoked by the API, deployment entrypoint or CI runtime.

Before running any legacy import:
1. Establish whether legacy mapping rows still exist and whether they have already been reconciled.
2. Export and independently review the mapping and duplicate/collision report.
3. Create and restore-test a PostgreSQL backup.
4. Verify the current native identity schema and migration checksums.
5. Run only once under an approved change record; inspect the reconciliation ledger afterward.
6. Require each imported account to complete the native password-reset and email-verification policy before sign-in.

If no verified mapping or source evidence exists, quarantine the account. Never infer identity from matching email addresses.

## Native staff identity

All new staff review, approval and WebAuthn credential records use Testagram staff principal UUIDs. Old provider reviewer IDs are historical evidence only. They must never satisfy a native staff authorization check or the independent two-person approval requirement.

## Migration immutability

Files under `migrations/` are an ordered, append-only history. Their historical filenames and SQL comments may mention the old provider because they describe what was applied at that point in time. Replace current behavior with new forward-only migrations; do not rewrite history to make a repository search appear clean.
