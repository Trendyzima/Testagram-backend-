# Instabox VPS watchdog

The watchdog runs from GitHub Actions, outside the Testagram VPS, every five minutes. It checks InstaBox machine state first, then probes public Testagram API liveness three times whenever the provider reports the machine running. Running machines are never restarted by this watchdog, even when all three application probes fail.

## Required GitHub configuration

The workflow defaults to the previously confirmed machine and API endpoint, so no non-secret variable is required for the initial setup. Optional repository **Actions variables** can override these defaults:

- `INSTABOX_MACHINE_NAME`: exact machine display name (default: `testagramdb`). The watchdog resolves it to an immutable ID through the read-only machine-list API and refuses to act if the match is missing or ambiguous.
- `INSTABOX_MACHINE_ID`: exact immutable machine ID, if you prefer pinning the resource directly. When supplied, the script also verifies the configured name against the returned machine.
- `TESTAGRAM_HEALTH_URL`: default `https://api.testagram.site/healthz`
- `TESTAGRAM_READINESS_URL`: default `https://api.testagram.site/readyz`

Optional repository **Actions variable**:

- `WATCHDOG_AUTO_RECOVERY_ENABLED`: defaults to `true` because automatic resume/start has already been explicitly approved for this machine. Set it to `false` to disable lifecycle recovery. Manual workflow runs still default to dry-run unless `dry_run=false` is selected.

Set this repository **Actions secret**:

- Reuse the existing InstaBox API credential stored as either `INSTABOX_WATCHDOG_API_KEY` or `INSTABOX_API_KEY` in GitHub Actions secrets. The workflow checks the first name and falls back to the second. The credential must belong to the workspace containing `testagramdb` and have machine read plus lifecycle-write permissions. Never put credentials in a repository variable, source file, issue, or workflow log.

Optional secret:

- `WATCHDOG_ALERT_WEBHOOK`: an incoming webhook endpoint for recovery/failure alerts. Do not use a webhook URL that grants broad account access.

## Recovery policy

1. Resolve the exact machine name to one immutable ID through InstaBox's machine list, then read that machine. A missing/duplicate name, mismatched ID or malformed response fails closed.
2. If the machine is stopped while desiredState=running, start it and verify the provider operation and public API liveness.
3. If the machine is suspended, force-resume it using the existing watchdog credential even when desiredState reports suspended, as explicitly authorized for this deployment. Verify the provider operation and final machine state before checking the application.
4. If the machine is running, probe the public API /healthz three times, 15 seconds apart. A single successful probe clears the failure streak. Three consecutive failures trigger a fresh provider-state read. Only if the same machine is still running, has no active lifecycle operation, and still has desiredState=running may the watchdog perform a controlled stop followed by start.
5. Poll every accepted lifecycle operation to completion (five-minute bound) and verify the expected provider state. Then check public API liveness separately. A 404 is reported as an application routing/probe-contract defect and does not erase a confirmed VPS resume; 5xx/timeouts remain application recovery failures.
6. Persist the last recovery action and health-failure evidence between scheduled runs. Enforce a 15-minute recovery cooldown. Serialize workflow runs to prevent concurrent lifecycle actions.
7. Never delete, recreate, restore, resize, or alter machine disks or database volumes. A provider error state, deleted machine, active operation, API authorization failure, billing restriction, or provider cooldown is surfaced as a failed watchdog run (not a successful no-op) and retried on the next scheduled check; provider cooldowns are never bypassed.
8. Do not run database migrations, docker compose down, volume deletion, image pruning, or data restoration as part of watchdog recovery.

This deployment explicitly authorizes forced resume for the `testagramdb` machine when InstaBox reports `suspended`, including when desiredState also reports suspended. The watchdog does not recreate deleted machines or bypass provider cooldowns, active operations, authorization failures, or billing restrictions. Application liveness failures never trigger a stop/start while the provider reports the machine running.

## Validation and activation

The workflow is activated by merging this change to the default branch. GitHub scheduled workflows have a five-minute minimum interval and may start later under platform load. Run it manually once after setting the required secret; manual runs default to **dry-run mode**. Scheduled runs use automatic recovery by default; set `WATCHDOG_AUTO_RECOVERY_ENABLED=false` to disable it. Only enable automatic recovery after replacing the old full-access key with the restricted key and confirming the exact machine and healthy endpoints.

Official API reference: https://instabox.sh/docs/api.md
