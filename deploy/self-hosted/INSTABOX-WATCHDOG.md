# Instabox VPS watchdog

The watchdog runs from GitHub Actions, outside the Testagram VPS, every five minutes. It uses the official Instabox API and checks both Testagram liveness and readiness before taking recovery action.

## Required GitHub configuration

The workflow defaults to the previously confirmed machine and API endpoint, so no non-secret variable is required for the initial setup. Optional repository **Actions variables** can override these defaults:

- `INSTABOX_MACHINE_NAME`: exact machine display name (default: `testagramdb`). The watchdog resolves it to an immutable ID through the read-only machine-list API and refuses to act if the match is missing or ambiguous.
- `INSTABOX_MACHINE_ID`: exact immutable machine ID, if you prefer pinning the resource directly. When supplied, the script also verifies the configured name against the returned machine.
- `TESTAGRAM_HEALTH_URL`: default `https://8080-agq4me2xgrgx5ge52ts437tqea.instabox.tech/healthz`
- `TESTAGRAM_READINESS_URL`: default `https://8080-agq4me2xgrgx5ge52ts437tqea.instabox.tech/readyz`

Set this repository **Actions secret**:

- `INSTABOX_API_KEY`: a newly created, restricted Instabox API key scoped to the workspace containing the machine, with only `machines:read` and `machines:write`. Do not reuse the previously saved full-access key; rotate/revoke that key and replace the GitHub secret. Never put credentials in a repository variable, source file, issue, or workflow log.

Optional secret:

- `WATCHDOG_ALERT_WEBHOOK`: an incoming webhook endpoint for recovery/failure alerts. Do not use a webhook URL that grants broad account access.

## Recovery policy

1. Resolve the exact machine name to one immutable ID through Instabox's read-only machine list, then read that machine. A missing/duplicate name, mismatched ID or malformed response fails closed.
2. If stopped, request `POST /v1/machines/{id}/start`.
3. If suspended, request `POST /v1/machines/{id}/resume`.
4. If running, probe both `/healthz` and `/readyz` three times, 15 seconds apart. If all probes fail, re-read the machine and confirm it is still running, has no active lifecycle operation, and still has `desiredState=running`. Only then perform a controlled stop followed by start.
5. Poll each accepted operation until success/failure, with a five-minute bound, and verify the provider reports the expected final machine state.
6. After start/resume/restart, wait up to five minutes for both public API health and readiness to return successfully before reporting recovery.
6. Persist a 30-minute cooldown specifically for disruptive stop/start restarts. Unexpected stopped machines are started again on the next scheduled check if a previous start attempt fails; suspended machines are similarly resumed again. Serialize runs to prevent concurrent restarts.
8. Never delete, recreate, restore, resize, or alter machine disks. A provider `error` state, deleted machine, active cooldown, API authorization failure, billing restriction, or provider cooldown is surfaced for operator action rather than bypassed.

The watchdog does not automatically override an intentional provider state where `desiredState` is not `running`. To keep a production workload continuously available, configure the Instabox machine's idle auto-suspend and maximum-run policy appropriately in the provider dashboard; the watchdog does not silently change billing or lifecycle policy.

## Validation and activation

The workflow is activated by merging this change to the default branch. GitHub scheduled workflows have a five-minute minimum interval and may start later under platform load. Run it manually once after setting the required variables and secret; manual runs default to **dry-run mode**, which checks the provider and endpoints without issuing lifecycle actions. Only disable dry-run after confirming the exact machine ID and both URLs. Scheduled runs use automatic recovery.

Official API reference: https://instabox.sh/docs/api.md
