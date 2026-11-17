# Instabox VPS watchdog

The watchdog runs from GitHub Actions, outside the Testagram VPS, every five minutes. It checks InstaBox machine state first, then public Testagram API liveness whenever the provider reports the machine running.

## Required GitHub configuration

The workflow defaults to the previously confirmed machine and API endpoint, so no non-secret variable is required for the initial setup. Optional repository **Actions variables** can override these defaults:

- `INSTABOX_MACHINE_NAME`: exact machine display name (default: `testagramdb`). The watchdog resolves it to an immutable ID through the read-only machine-list API and refuses to act if the match is missing or ambiguous.
- `INSTABOX_MACHINE_ID`: exact immutable machine ID, if you prefer pinning the resource directly. When supplied, the script also verifies the configured name against the returned machine.
- `TESTAGRAM_HEALTH_URL`: default `https://api.testagram.site/healthz`
- `TESTAGRAM_READINESS_URL`: default `https://api.testagram.site/readyz`

Set this repository **Actions variable**:

- `WATCHDOG_AUTO_RECOVERY_ENABLED`: leave unset or set to `false` during setup. Scheduled runs remain in dry-run mode until you explicitly set it to `true` after installing the restricted API key and confirming the machine and endpoints.

Set this repository **Actions secret**:

- `INSTABOX_WATCHDOG_API_KEY`: a newly created, restricted Instabox API key scoped to the workspace containing the machine, with only `machines:read` and `machines:write`. Do not reuse the previously saved full-access key; rotate/revoke that key and replace the GitHub secret. Never put credentials in a repository variable, source file, issue, or workflow log.

Optional secret:

- `WATCHDOG_ALERT_WEBHOOK`: an incoming webhook endpoint for recovery/failure alerts. Do not use a webhook URL that grants broad account access.

## Recovery policy

1. Resolve the exact machine name to one immutable ID through InstaBox's machine list, then read that machine. A missing/duplicate name, mismatched ID or malformed response fails closed.
2. If the machine is stopped while desiredState=running, start it and verify the provider operation and public API liveness.
3. If the machine is suspended, treat it as an intentional idle pause. Do not send scheduled wake requests or force-resume it: preserve InstaBox's real-request/SSH auto-wake behavior.
4. If the machine is running, probe the public API /healthz three times, 15 seconds apart. A single successful probe clears the failure streak. Three consecutive failures trigger a fresh provider-state read. Only if the same machine is still running, has no active lifecycle operation, and still has desiredState=running may the watchdog perform a controlled stop followed by start.
5. Poll every accepted lifecycle operation to completion (five-minute bound), verify the expected provider state, then wait up to five minutes for public API liveness to return.
6. Persist the last recovery action and health-failure evidence between scheduled runs. Enforce a 15-minute recovery cooldown. Serialize workflow runs to prevent concurrent lifecycle actions.
7. Never delete, recreate, restore, resize, or alter machine disks or database volumes. A provider error state, deleted machine, active operation/cooldown, API authorization failure, billing restriction, or provider cooldown is surfaced for operator action rather than bypassed.
8. Do not run database migrations, docker compose down, volume deletion, image pruning, or data restoration as part of watchdog recovery.

The watchdog deliberately does not override an intentional provider state where desiredState is not running. Suspended idle machines are allowed to wake on real requests. The controlled stop/start path is reserved for a machine that the provider still reports as running but whose public API liveness has failed three times.

## Validation and activation

The workflow is activated by merging this change to the default branch. GitHub scheduled workflows have a five-minute minimum interval and may start later under platform load. Run it manually once after setting the required secret; manual runs default to **dry-run mode**. Scheduled runs also remain in dry-run mode until `WATCHDOG_AUTO_RECOVERY_ENABLED=true`. Only enable automatic recovery after replacing the old full-access key with the restricted key and confirming the exact machine and healthy endpoints.

Official API reference: https://instabox.sh/docs/api.md
