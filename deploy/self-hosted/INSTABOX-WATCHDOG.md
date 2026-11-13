# Instabox VPS watchdog

The watchdog runs from GitHub Actions, outside the Testagram VPS, every five minutes. It uses the official Instabox API and checks both Testagram liveness and readiness before taking recovery action.

## Required GitHub configuration

Set these repository **Actions variables**:

- `INSTABOX_MACHINE_ID`: exact immutable machine ID from the Instabox API (not the display name).
- `TESTAGRAM_HEALTH_URL`: public HTTPS URL for the API `/healthz` endpoint.
- `TESTAGRAM_READINESS_URL`: public HTTPS URL for the API `/readyz` endpoint.

Set this repository **Actions secret**:

- `INSTABOX_API_KEY`: an Instabox API key scoped to the workspace containing the machine, with `machines:read` and `machines:write`. Never put it in a repository variable, source file, issue, or workflow log.

Optional secret:

- `WATCHDOG_ALERT_WEBHOOK`: an incoming webhook endpoint for recovery/failure alerts. Do not use a webhook URL that grants broad account access.

## Recovery policy

1. Read the exact machine by immutable ID. A mismatched ID or malformed response fails closed.
2. If stopped, request `POST /v1/machines/{id}/start`.
3. If suspended, request `POST /v1/machines/{id}/resume`.
4. If running, probe both `/healthz` and `/readyz` three times, 15 seconds apart. If all probes fail, re-read the machine and confirm it is still running, has no active lifecycle operation, and still has `desiredState=running`. Only then perform a controlled stop followed by start.
5. Poll each accepted operation until success/failure, with a five-minute bound. Never issue a second lifecycle operation while one is active.
6. Persist a 30-minute action cooldown across scheduled runs. Serialize runs to prevent concurrent restarts.
7. Never delete, recreate, restore, resize, or alter machine disks. A provider `error` state, deleted machine, active cooldown, API authorization failure, billing restriction, or provider cooldown is surfaced for operator action rather than bypassed.

The watchdog does not automatically override an intentional provider state where `desiredState` is not `running`. To keep a production workload continuously available, configure the Instabox machine's idle auto-suspend and maximum-run policy appropriately in the provider dashboard; the watchdog does not silently change billing or lifecycle policy.

## Validation and activation

The workflow is activated by merging this change to the default branch. GitHub scheduled workflows have a five-minute minimum interval and may start later under platform load. Run it manually once after setting the required variables and secret. Confirm the first run reports the expected machine ID and healthy endpoints before relying on automatic recovery.

Official API reference: https://instabox.sh/docs/api.md
