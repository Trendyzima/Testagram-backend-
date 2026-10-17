# Testagram self-hosted backup and restore

This runbook backs up PostgreSQL metadata and both MinIO media buckets into an
encrypted [restic](https://restic.net/) repository. Valkey is intentionally not
backed up: its cache and queue data are operational state and must be rebuilt or
replayed according to each consumer's recovery policy. Do not treat this as a
complete disaster-recovery plan until an off-host repository and a restore drill
have both been verified.

## Requirements

- Run from a trusted operator machine with Docker Compose v2, Bash, and restic.
- The Compose stack must be running and healthy.
- Configure an encrypted restic repository on a different machine or provider.
  A repository on the same disk/host is useful for snapshots but is not disaster
  recovery.
- Store the restic password in a dedicated file readable only by the operator:
  `chmod 600 /path/to/restic-password`. Keep a second copy of the password in
  a separate secure password manager; losing it makes the repository
  unrecoverable.
- Ensure the machine has enough temporary disk space for a PostgreSQL dump and
  a full working copy of both media buckets. The script uses a mode-0700
  temporary directory and deletes it on exit, but the staged files are plaintext
  while the backup runs. Use an encrypted host disk and a protected temporary
  filesystem.

## Initialize and run

From the repository root:

```sh
# Choose an off-host repository appropriate to your restic backend.
export RESTIC_REPOSITORY='sftp:backup-user@backup-host:/srv/restic/testagram'
export RESTIC_PASSWORD_FILE="$HOME/.config/testagram/restic-password"
chmod 600 "$RESTIC_PASSWORD_FILE"

# Run once only for a brand-new repository; do not reinitialize an existing one.
restic init

# Create a backup and verify a subset of stored data.
./scripts/self-hosted-backup.sh backup

# Verify repository integrity later.
./scripts/self-hosted-backup.sh check

# Apply 7 daily, 5 weekly, and 12 monthly retention points.
./scripts/self-hosted-backup.sh prune
```

For unattended scheduling, configure the same environment in a root-owned
systemd service/timer or a protected cron environment. Do not put the repository
password directly in the unit file, command line, or repository. Send job
stdout/stderr and exit status to the operator alerting channel. Run backup at
least daily and run the integrity check on a separate schedule. Review available
space on both the application host and backup destination.

The script requires a password file with mode 0600 and refuses to run if it is
more permissive. For S3-compatible restic repositories, use the documented
restic environment variables and a dedicated least-privilege backup identity.
Do not reuse MinIO root credentials for an off-host backup destination.

## Restore drill (never experiment on production)

1. Provision a clean, isolated recovery host and install compatible Docker,
   PostgreSQL client tools, MinIO client, and restic.
2. Configure `RESTIC_REPOSITORY` and `RESTIC_PASSWORD_FILE` for the backup
   repository, then run `./scripts/self-hosted-backup.sh check`.
3. List snapshots with `restic snapshots --tag testagram`, select a known
   snapshot ID, and restore it to an empty recovery directory:
   `restic restore <snapshot-id> --target /secure/restore`.
4. Inspect the restored manifest and validate the database dump:
   `pg_restore --list /secure/restore/<snapshot-path>/postgres.dump`.
   The restored path includes the original staging directory name; locate it
   with `find /secure/restore -name postgres.dump -print`.
5. Start a disposable PostgreSQL instance with no public ports, create an empty
   target database, and restore with `pg_restore --clean --if-exists --no-owner
   --no-acl --dbname="$RECOVERY_DATABASE_URL" /path/to/postgres.dump`. Review
   the target and permissions before executing; never point this command at the
   live production database during a drill.
6. Restore private/public bucket contents into empty test buckets using
   `mc mirror` from the recovered directories. Verify object counts and sample
   hashes against the source where feasible.
7. Run application health checks and media-read checks against the isolated
   recovery stack. Record duration, missing data, recovery point (RPO), recovery
   time (RTO), and corrective actions in the operations log.
8. Securely remove plaintext restore material after sign-off and preserve the
   drill record. Repeat after schema, object-store, or backup-process changes.

## Scope and operational limits

- The backup includes PostgreSQL and objects in the configured private and
  public MinIO buckets. It does not back up Docker images, host configuration,
  DNS, secrets, TLS private keys, or the Valkey queue state.
- Back up the deployment configuration and secrets separately in a secure
  password manager/off-host secret store. Never commit real secrets.
- The script does not create an off-site repository for you, provision backup
  credentials, configure alerts, or prove a successful restore automatically.
- Run a restore drill before production cutover and periodically thereafter.
  A successful backup command is not proof of recoverability.
