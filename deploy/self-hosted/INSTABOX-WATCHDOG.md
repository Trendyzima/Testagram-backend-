# InstaBox VPS watchdog

The watchdog runs from GitHub Actions, outside the Testagram VPS, every five minutes. It checks the InstaBox provider state first, then probes public Testagram API liveness three times (15 seconds apart) whenever the provider reports the machine running. Running machines are never restarted by this watchdog, even when all application probes fail.

## Required GitHub configuration

The workflow defaults to the previously confirmed machine and API endpoint. Optional repository **Actions variables** can override these defaults:

- `INSTABOX_MACHINE_NAME`: exact machine display name (default: `testagramdb`). The watchdog resolves it to an immutable ID through the machine-list API and refuses to act if the match is missing or ambiguous.
- `INSTABOX_MACHINE_ID`: exact immutable machine ID, if you prefer pinning the resource directly. When supplied, the script also verifies the configured name against the returned machine.
- `TESTAGRAM_HEALTH_URL`: default `https://api.testagram.site/health`.
- `WATCHDOG_AUTO_RECOVERY_ENABLED`: defaults to `true` for the previously authorized automatic start/resume policy. Set it to `false` to disable lifecycle recovery. Manual workflow runs default to dry-run mode.

Set this repository **Actions secret**:

- Reuse the existing InstaBox API credential stored as either `INSTABOX_WATCHDOG_API_KEY` or `INSTABOX_API_KEY` in GitHub Actions secrets. The workflow checks the first name and falls back to the second. The credential must belong to the workspace containing `testagramdb` and have machine-read plus lifecycle-write permissions. Never put credentials in a repository variable, source file, issue, or workflow log.

Optional secret:

- `WATCHDOG_ALERT_WEBHOOK`: an incoming webhook endpoint for recovery/failure alerts. Do not use a webhook URL that grants broad account access.

## Recovery and monitoring policy

1. Resolve the exact machine name to one immutable ID through InstaBox's machine list, then read that machine. A missing/duplicate name, mismatched ID, or malformed response fails closed.
2. If the machine is `stopped`, request `start` and verify the provider operation and final machine state. This recovery applies even when `desiredState` also reports `stopped`, as explicitly authorized for this machine.
3. If the machine is `suspended`, request `resume` using the existing InstaBox credential, even when `desiredState` reports `suspended`, as explicitly authorized for this deployment. Verify the provider operation and final machine state before checking the application.
4. If the machine is `running`, probe the public API `/health` up to three times, 15 seconds apart. A successful probe clears the failure streak. Three failed probes are reported and alerted; they do **not** trigger stop/start or any other lifecycle action against a provider-running machine.
5. If a lifecycle operation is already active, leave it undisturbed. Poll each accepted operation to completion (five-minute bound) and verify the expected provider state. Application liveness is checked separately after recovery; an HTTP 404 is a probe/routing contract defect, not proof that provider resume failed.
6. Persist recovery/health incident state between scheduled runs. Enforce a 15-minute recovery cooldown and serialize workflow runs to prevent concurrent lifecycle actions.
7. Provider cooldowns, API authorization failures, billing restrictions, malformed responses, missing/duplicate machine identity, and provider errors are surfaced as failed watchdog checks and retried by a later scheduled run. Never bypass provider controls.
8. Never delete, recreate, restore, resize, or alter machine disks or database volumes. Do not run database migrations, `docker compose down`, volume deletion, image pruning, or data restoration as part of watchdog recovery.

This deployment explicitly authorizes starting the `testagramdb` machine when InstaBox reports `stopped` and resuming it when InstaBox reports `suspended`. The watchdog does not recreate deleted machines, interrupt active operations, bypass provider cooldowns, or restart a machine that the provider reports as running.

## Validation and activation

The workflow is already present on the default branch and is scheduled every five minutes. GitHub may delay scheduled starts under platform load, so this is not a guaranteed real-time or sub-five-minute recovery service. Manual workflow runs default to **dry-run mode**; scheduled runs use the configured automatic-recovery switch. Set `WATCHDOG_AUTO_RECOVERY_ENABLED=false` to disable automatic lifecycle recovery.

A successful workflow run proves the provider API interaction and checks completed as reported in that run; it does not guarantee that the VPS can never be suspended again or that the application is reachable from every network. Confirm recovery using the workflow run log and provider state.

Official API reference: https://instabox.sh/docs/api.md
