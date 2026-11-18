# InstaBox production inspection boundaries

Testagram deliberately separates machine metadata access from host command execution.

## 1. GitHub Actions: strict read-only machine metadata

Run **Read-only InstaBox machine status** from GitHub Actions for a basic machine-state check. It uses only:

- `GET /v1/machines?limit=100` when the machine ID is not configured.
- `GET /v1/machines/{id}` to inspect the selected machine's state and runtime readiness.

Configure the existing repository secret `INSTABOX_READONLY_API_KEY` with the InstaBox `machines:read` scope. Keep this key read-only. Do not replace it with `INSTABOX_API_KEY`, `INSTABOX_BACKUP_API_KEY`, a deployment key, or any key that can execute commands.

Set `INSTABOX_MACHINE_ID` as a repository secret when the target machine ID is known. Otherwise set the non-sensitive `INSTABOX_MACHINE_NAME` repository variable. The workflow resolves the name only when it finds one unambiguous match.

This GitHub workflow does not call `/exec`, wake or resume a machine, access Docker, or query PostgreSQL. It cannot prove container health or database schema readiness.

## 2. Docker and PostgreSQL: operator-run inspection on the VPS

Deep host inspection is intentionally not run through the read-only API key or GitHub Actions. An authorized operator with an existing VPS shell session should run the script locally on the machine:

```sh
cd /data/home/testagram/repos/backend
bash deploy/self-hosted/inspect-production-readonly.sh
```

The operator must already have the repository checkout and local permissions to inspect Docker. The script uses `sudo` where required. It does not call the InstaBox API and does not transmit a command through GitHub Actions.

It inspects:

- Testagram Backend and Data Centre Compose container state, health, restart counts, exit codes and OOM status.
- Compose service status, Docker daemon summary and a one-time Docker resource snapshot.
- Native identity PostgreSQL schema readiness through the existing migration helper's `pg_isready` and schema-only `SELECT` preflight.

The schema helper is invoked with `CONFIRM_NATIVE_IDENTITY_MIGRATIONS=` explicitly empty. If the schema is incomplete, the helper exits before creating a backup or applying migrations. This inspection script must never be run with a migration confirmation value.

The script does not start, stop, restart, wake, deploy, pull, recreate, prune or remove containers. It does not apply migrations or intentionally write application data. Docker/Compose inspection and PostgreSQL connection checks may still create ordinary daemon/API activity and database connection log entries; "read-only" describes the inspected application state, not a claim of zero operational side effects.

## 3. Privileged remote inspection is not enabled

Do not add a `machines:exec` key to GitHub Actions as a workaround. InstaBox command execution can run arbitrary commands on the host and is therefore privileged access, even if a particular command is designed not to modify state.

If a future operational requirement justifies remote execution, it must be a separate explicitly approved workflow, use a dedicated key, be labelled **Privileged host inspection**, require manual approval, and be audited independently. It must never reuse `INSTABOX_READONLY_API_KEY` or production deployment credentials.

## 4. Interpreting results

A successful machine-status workflow proves only that the read-scoped key can read the selected machine metadata and that the machine reports running/ready. It is not a production acceptance test.

A successful operator inspection provides a point-in-time host/container/schema report. It does not prove public registration, end-to-end login, media replication to Cloudflare R2 and Backblaze B2, backup recoverability, or user-facing request success. Those require their own controlled staging and production acceptance tests.
