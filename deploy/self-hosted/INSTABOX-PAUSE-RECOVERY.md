# InstaBox machine-pause recovery runbook

**Scope:** Testagram's current InstaBox Free machine, observed as `machine-paused`.
**Purpose:** Restore service safely after the provider pauses the machine. This runbook does not keep a Free machine awake or bypass provider limits.

## Important operating boundary

- InstaBox Free automatically pauses an idle machine. This is a provider lifecycle event, not a container crash.
- Do not use scheduled pings, busy loops, activity leases, or repeated pause/resume cycles to evade the Free-plan limit.
- `restart: unless-stopped` helps recover containers after the VM is running; it cannot resume a paused VM.
- Treat the Free machine as development/staging only. Do not claim production availability until Testagram is on an approved always-on plan/host and external recovery tests pass.

## 1. Confirm the provider state

From an operator workstation with the InstaBox CLI authenticated:

```sh
instabox usage
instabox --help
```

Confirm the machine named `testagramdb` is paused in the InstaBox dashboard/CLI. Check the InstaBox status page before treating a provider-wide incident as a local machine issue:

- https://status.instabox.sh

If the machine is paused, resume it using the supported lifecycle operation:

```sh
instabox resume testagramdb
```

If it is stopped rather than paused, inspect the machine state and use the documented `instabox start testagramdb` operation. Do not use `restart` as the first response to a normal pause.

## 2. Verify persistent storage before application recovery

Connect to the resumed machine and move to the checked-out Backend deployment directory. Confirm the expected persistent PostgreSQL volume is present and mounted before starting or recreating containers.

```sh
docker compose --env-file deploy/self-hosted/.env -f deploy/self-hosted/docker-compose.yml ps
docker volume ls
```

Run these from the repository root. If the deployment uses a different working directory or Compose project name, use that deployment's established values; do not create a new empty volume.

**Stop and escalate** if the PostgreSQL data volume is missing, unexpectedly empty, detached, or replaced. Never run `docker compose down -v`, remove volumes, reinitialize PostgreSQL, or restore a snapshot over the live database as a routine recovery step.

## 3. Recover services in dependency order

Use the deployment's existing Compose project and environment file. First inspect configuration and current state:

```sh
docker compose --env-file deploy/self-hosted/.env -f deploy/self-hosted/docker-compose.yml config --quiet
docker compose --env-file deploy/self-hosted/.env -f deploy/self-hosted/docker-compose.yml ps
```

If containers are stopped but the persistent volume is verified, start the existing stack:

```sh
docker compose --env-file deploy/self-hosted/.env -f deploy/self-hosted/docker-compose.yml up -d
```

Do not add `--renew-anon-volumes`, `-V`, `down -v`, or destructive database initialization flags.

Inspect service logs and wait for PostgreSQL readiness before evaluating API readiness:

```sh
docker compose --env-file deploy/self-hosted/.env -f deploy/self-hosted/docker-compose.yml logs --since=15m postgres
docker compose --env-file deploy/self-hosted/.env -f deploy/self-hosted/docker-compose.yml exec -T postgres pg_isready
docker compose --env-file deploy/self-hosted/.env -f deploy/self-hosted/docker-compose.yml ps
```

Then probe the public API from outside the machine (not from localhost alone):

```sh
curl --fail --show-error --silent https://api.testagram.site/healthz
curl --fail --show-error --silent https://api.testagram.site/readyz
```

A process being `Up` is not sufficient. Readiness must pass, and the external probe must succeed.

## 4. Post-resume integrity and queue checks

Before declaring recovery complete:

1. Inspect PostgreSQL logs for crash recovery, WAL replay errors, corruption, read-only state, or failed checkpoints.
2. Verify the latest successful off-machine backup and its integrity record. If no independently stored backup exists, record this as a production-blocking incident; do not assume an InstaBox snapshot is an independent backup.
3. Inspect durable outbox/worker queues for expired leases and stuck in-progress jobs using each service's documented operator queries.
4. Reconcile provider callbacks and payment records before retrying any wallet or M-Pesa operation. Never manually credit a wallet to compensate for a timeout.
5. Verify Federation, Data Centre workers, media replication, CDN origin delivery and web/API smoke tests.
6. Record incident start/end, provider state, recovery actions, data checks, and any jobs requiring reconciliation.

Do not run unreviewed SQL updates against ledger, outbox, or migration tables during recovery.

## 5. Escalation conditions

Stop automated recovery and escalate to the database/operator owner if:

- the expected PostgreSQL volume is absent or has an unexpected identity;
- PostgreSQL cannot complete crash recovery or reports corruption;
- migrations are pending or failed unexpectedly;
- backups are missing, stale, or fail verification;
- duplicate payment, wallet-ledger, or outbox effects are suspected;
- the machine repeatedly pauses/stops or InstaBox reports a platform incident.

## 6. Production exit criteria

The Free plan's automatic pause cannot be disabled by Testagram software. Before serving production traffic:

- upgrade to an InstaBox paid plan and explicitly set idle auto-suspend to `0` using the supported CLI/dashboard setting, or approve a separate always-on host;
- verify billing limits and usage alerts;
- configure external monitoring and alert delivery independent of this machine;
- enable encrypted off-machine PostgreSQL backups and WAL/PITR where supported;
- perform and record a restore drill;
- demonstrate pause/resume and process-restart recovery without data loss or duplicate side effects;
- publish measured RPO and RTO.

A paid InstaBox machine is still a hosted microVM. Disabling idle pause does not itself establish dedicated physical hardware, high availability, or an uptime SLA.

## Related tracking

Implementation and acceptance evidence: [Backend issue #442](https://github.com/Trendyzima/Testagram-backend-/issues/442).
