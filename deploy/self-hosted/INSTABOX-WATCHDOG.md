# InstaBox watchdog operations

The external watchdog is now **manual-dispatch only**. It no longer runs on a five-minute cron schedule or on pushes to the default branch. This avoids scheduled public health probes and automated pause/resume cycling on the Free plan.

## Normal Free-plan behavior

- InstaBox native wake-on-request handles genuine requests to published application ports when the provider idle-pauses the machine.
- Do not run scheduled health checks against the public app as a keepalive.
- Do not run automated `resume` or `start` cycles to defeat idle pause or maximum runtime.
- Use the separate read-only machine metadata workflow only when an operator deliberately requests a status check.

## Operator-dispatched recovery

The `Instabox VPS Watchdog` workflow can be started manually from GitHub Actions. It defaults to dry-run and `WATCHDOG_AUTO_RECOVERY_ENABLED=false`.

1. Confirm the provider state is actually `stopped` or manually `suspended`, not an ordinary idle pause that native wake-on-request should handle.
2. Confirm the reason for the state and that an operator-approved recovery is appropriate under the active InstaBox plan.
3. Run the workflow manually with dry-run enabled first.
4. Only if a lifecycle operation is genuinely required, explicitly set the repository variable `WATCHDOG_AUTO_RECOVERY_ENABLED=true` and dispatch with dry-run disabled. This is an operator action, not an unattended schedule.
5. Verify the provider operation result, public application liveness, PostgreSQL recovery, and durable-work reconciliation. Do not interpret provider state alone as application readiness.

The workflow uses the existing InstaBox credential stored in GitHub Actions secrets. Never print, paste, commit, or place that credential in Flutter, browser code, or public configuration.

## Scope and limits

- Running machines are never restarted by this watchdog.
- Active provider operations and provider cooldowns are respected.
- Machine identity is resolved by exact ID/name and lifecycle operations carry idempotency keys.
- A successful provider resume does not prove PostgreSQL, API, CDN, Federation, media replication, or wallet reconciliation are healthy.
- Free-plan idle pause and maximum-run limits remain in force. Production needs a paid always-on tier or an approved always-on host, external monitoring, off-machine encrypted backups, and tested restore procedures.
