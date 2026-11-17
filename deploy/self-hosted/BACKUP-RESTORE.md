# Testagram self-hosted backup and restore

This runbook backs up PostgreSQL metadata and all four MinIO buckets (private media, public media, identity evidence, and metadata) into an
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

The script accepts either a password file with mode 0600 (and refuses to
run if it is more permissive) or `RESTIC_PASSWORD` for a short-lived automated
job. Prefer the file for interactive operator runs; the InstaBox workflow passes
the password only as a temporary exec environment variable and never stores it
in the production `.env` or a command line. For S3-compatible restic
repositories, use the documented restic environment variables and a dedicated
least-privilege backup identity. Do not reuse MinIO root credentials for an
off-host backup destination.


## InstaBox free-plan production schedule

Testagram's current small-scale production machine is `testagramdb`. The free
plan intentionally pauses it after 30 minutes without activity; a run starts at
120 minutes and can extend through genuine use up to 480 minutes. Do not add
heartbeat pings, busy loops, or recurring activity leases to keep it awake.
Incoming requests should wake an idle-suspended machine through the Cloudflare
gateway. A real backup is legitimate work, so the scheduled backup below wakes
the machine only while it is exporting and verifying data, then leaves it free
to pause again.

The GitHub Actions workflow
`.github/workflows/instabox-production-backup.yml` is **disabled on schedule
until** the repository variable `TESTAGRAM_PRODUCTION_BACKUP_ENABLED` is set
to `true`. Manual dispatch is available after its credentials are configured.
It calls InstaBox's `POST /v1/machines/{id}/wake` and then `POST
/v1/machines/{id}/exec`; it never calls `start`, `resume`, `stop`, or
`resize`, and it does not bypass a max-run cooldown or a manual operator stop.

### Off-host destination and credentials

Create a **dedicated private S3-compatible bucket** for backups, separate from
the live media, identity-evidence, and metadata buckets. Cloudflare R2 or
Backblaze B2 can be used. Create a dedicated access key restricted to that
bucket; do not reuse the MinIO root key or the application's media key. Restic
encrypts the repository before uploading it. Keep the bucket private and do not
apply an object-expiration rule that can delete restic pack/index files behind
its back.

Configure these GitHub repository variables:

- `TESTAGRAM_BACKUP_S3_ENDPOINT`: optional HTTPS endpoint origin only. If
  omitted, the workflow uses the existing `B2_S3_ENDPOINT`, then
  `CLOUDFLARE_R2_ENDPOINT`. For R2 this is
  `https://<account-id>.r2.cloudflarestorage.com`; for B2 use the S3 endpoint
  for the bucket's region.
- `TESTAGRAM_BACKUP_S3_BUCKET`: required dedicated bucket name, e.g.
  `testagram-backups`.
- `TESTAGRAM_BACKUP_S3_REGION`: optional; defaults to the existing B2/R2
  region, then `auto` for R2. B2 uses its bucket region (e.g. `us-west-004`).
- `INSTABOX_MACHINE_ID`: optional; defaults to the verified current machine
  ID. Set it if the machine is ever replaced.
- `TESTAGRAM_PRODUCTION_BACKUP_ENABLED`: set to `true` only after the first
  manual backup and restore drill succeed.
- `TESTAGRAM_BACKUP_PRUNE_ENABLED`: leave unset/false until restore has been
  proven; set to `true` to apply the weekly retention policy.

Configure these GitHub repository secrets:

- `INSTABOX_BACKUP_API_KEY`: recommended dedicated InstaBox API key with
  `machines:write` (or legacy `machines:exec`) for this machine. If omitted,
  the workflow falls back to the existing `TESTAGRAM_DB_API` deployment
  secret. Do not paste either key into chat or commit it.
- `TESTAGRAM_BACKUP_S3_ACCESS_KEY_ID` and
  `TESTAGRAM_BACKUP_S3_SECRET_ACCESS_KEY`: recommended dedicated credentials
  scoped to the backup bucket. If omitted, the workflow can fall back to the
  existing B2 key, then R2 key, provided that key has access to the new bucket.
  A bucket-scoped key is strongly preferred over reusing a media key.
- `TESTAGRAM_BACKUP_RESTIC_PASSWORD`: a strong independent password; generate
  it locally with `openssl rand -hex 32` and store it in a password manager as
  well as GitHub Secrets. Losing it makes the encrypted repository
  unrecoverable.

The workflow constructs the repository as
`s3:<endpoint>/<bucket>/testagram`. It passes credentials only to the
single InstaBox exec request as temporary environment variables; it does not
write the S3 keys or restic password into the machine's persistent `.env`.
The remote helper refreshes only the backup scripts from GitHub main before
running them. It does not reset the production checkout, change Compose files,
run migrations, or restart application containers.

### First run and ongoing operation

1. Create the private bucket and scoped S3 key, then add the variables/secrets
   above. Keep `TESTAGRAM_PRODUCTION_BACKUP_ENABLED` unset/false for now.
2. In Actions, run **InstaBox production backup (free-plan safe)** manually
   with `run_restore_drill=true`. This creates the first encrypted backup and
   restores it into a disposable PostgreSQL container with no network or
   published ports. It never restores over production.
3. Confirm the run reports a successful restore drill. Only then set
   `TESTAGRAM_PRODUCTION_BACKUP_ENABLED=true` for one daily backup at 02:17 UTC.
4. After several successful backups and a restore drill, optionally set
   `TESTAGRAM_BACKUP_PRUNE_ENABLED=true`. Sunday runs then retain 7 daily, 5
   weekly, and 12 monthly points. Manual dispatch can also request a prune.
5. Check the workflow result and the provider's bucket usage. The first backup
   copies the current PostgreSQL database and both MinIO media buckets; later
   Restic runs upload only changed data. A failed or timed-out exec is not
   blindly retried because InstaBox exec is not idempotent; inspect machine
   history before retrying.

This workflow does not keep the machine awake. It is a genuine, bounded backup
task, and the machine can return to its normal 30-minute idle-pause behavior
after it completes.

The current helper deliberately fails closed if `DATA_CENTRE_MEDIA_ENABLED`,
`MEDIA_REPLICATION_ENABLED`, or `FRESH_MEDIA_ENABLED` is enabled in the
production `.env`. In that configuration, external B2/R2 object storage may
contain canonical or replicated bytes that are not in MinIO. Do not disable
that guard or declare backups complete until a separate encrypted mirror for
every active external bucket has been added and restore-tested. With the
current local MinIO configuration, the backup includes private media, public
media, identity-evidence objects, and the metadata sidecar bucket. The free plan has a fixed 20 GiB machine disk and includes
100 GiB of egress per month; monitor the first backup and storage-provider
usage, especially if media grows. InstaBox's free plan also has a 200 GiB
stored-data cap and five snapshots. This workflow does not create machine
snapshots or consume those five snapshot slots.


## Restore drill (never experiment on production)

From the repository root, the automated database restore check is:

```sh
./scripts/self-hosted-restore-drill.sh
```

It restores the latest `testagram` snapshot to a private temporary directory,
starts a disposable PostgreSQL container with `--network none` and no published
ports, restores the dump into that database, and checks that application tables
exist. It reports the number of recovered private/public media files and
securely removes its temporary directory, container, and volume on exit. The
operator must have enough free disk space for a full temporary snapshot restore
and Docker volume for the restored database.

This automated check does **not** restore media files into a running MinIO
instance, test public/private URLs, or measure production traffic recovery. Use
the full manual drill below before cutover and periodically thereafter.

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
