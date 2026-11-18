# Testagram self-hosted backup and restore

This runbook backs up every connectable non-template PostgreSQL database, all four MinIO buckets
(private media, public media, identity evidence, and metadata), plus detected
Data Centre video and node-storage directories, into an encrypted [restic](https://restic.net/) repository. Valkey is intentionally not
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
- Ensure the machine has enough temporary disk space for a PostgreSQL dump,
  all four MinIO buckets, and any detected Data Centre video/node-storage files. The script uses a mode-0700
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

The InstaBox wrapper stages temporary plaintext under `/data/tmp` by default,
not RAM-backed `/tmp`. An optional `BACKUP_TMP_PARENT` may select another
directory only if it resolves inside `/data`; the wrapper rejects other paths.
The directory is mode 0700, removed on normal exit, and stale scratch data is
cleared before the next locked run.


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
The workflow uses `INSTABOX_MACHINE_ID` when supplied; otherwise it resolves
the machine named `testagramdb`, falling back to the sole account machine only
when exactly one exists. It does not guess or hard-code a machine ID. Its GitHub
concurrency group is shared with the production deployment workflow, so a backup
and deployment cannot operate on the machine simultaneously.
It calls InstaBox's `POST /v1/machines/{id}/wake` and then `POST
/v1/machines/{id}/exec`; it never calls `start`, `resume`, `stop`, or
`resize`, and it does not bypass a max-run cooldown or a manual operator stop.

### Off-host destination and credentials

The InstaBox backup workflow uses the repository's existing Backblaze B2 S3
configuration as its primary encrypted Restic destination:

- `B2_S3_ENDPOINT`
- `B2_S3_BUCKET`
- `B2_S3_REGION`
- `B2_S3_ACCESS_KEY_ID`
- `B2_S3_SECRET_ACCESS_KEY`

The Restic repository is isolated under the `testagram-backups` object-key
prefix inside that configured bucket. This avoids requiring a new bucket
variable and prevents Restic's repository files from being written into the
application's normal object-key namespace. The same B2 credentials are reused
because these are the configured credentials already present in this repository;
they are not copied into the production `.env`. For stronger least-privilege
isolation, the B2 application key should be restricted to the backup prefix
when the provider supports that restriction.

The workflow can fall back to the existing dedicated Cloudflare R2 backup
tuple only if all of its endpoint, bucket, and matching R2 access-key values
are present. It never mixes B2 keys with an R2 endpoint or vice versa.

The workflow also requires:

- `TESTAGRAM_BACKUP_RESTIC_PASSWORD`: independent encryption password for the
  Restic repository. Losing it makes the encrypted repository unrecoverable.
- `INSTABOX_BACKUP_API_KEY`: dedicated InstaBox key with permission to wake
  and execute on the production machine. If absent, the workflow uses the
  existing `TESTAGRAM_DB_API` secret.

No new GitHub variables are required for the selected B2 configuration. The
workflow validates the endpoint, bucket and region before waking the machine.
It keeps repository initialization an explicit manual action and never
reinitializes after a transient read failure.

### First run and ongoing operation

1. Create the private bucket and scoped S3 key, then add the variables/secrets
   above. Keep `TESTAGRAM_PRODUCTION_BACKUP_ENABLED` unset/false for now.
2. In Actions, run **InstaBox production backup (free-plan safe)** manually.
   On the very first run only, set `initialize_restic_repository=true` after
   confirming the dedicated bucket/prefix is empty and reserved for this
   Testagram repository. Also set `run_restore_drill=true`. The workflow
   explicitly initializes Restic, creates the encrypted backup, then restores
   it into a disposable PostgreSQL container with no network or published
   ports. It never restores over production. Leave initialization false on
   every later run; the workflow refuses to initialize implicitly.
3. Confirm the run reports a successful restore drill. Only then set
   `TESTAGRAM_PRODUCTION_BACKUP_ENABLED=true` for one daily backup at 02:17 UTC.
4. After several successful backups and a restore drill, optionally set
   `TESTAGRAM_BACKUP_PRUNE_ENABLED=true`. Sunday runs then retain 7 daily, 5
   weekly, and 12 monthly points. Manual dispatch can also request a prune.
5. Check the workflow result and the provider's bucket usage. The first backup
   copies all connectable non-template PostgreSQL databases, all four MinIO buckets, and detected local Data Centre files; later
   Restic runs upload only changed data. A failed or timed-out exec is not
   blindly retried because InstaBox exec is not idempotent; inspect machine
   history before retrying.

This workflow does not keep the machine awake. It is a genuine, bounded backup
task, and the machine can return to its normal 30-minute idle-pause behavior
after it completes.

The current helper deliberately fails closed if `DATA_CENTRE_MEDIA_ENABLED`,
`MEDIA_REPLICATION_ENABLED`, or `FRESH_MEDIA_ENABLED` is enabled; if
`MEDIA_S3_ENDPOINT`, legacy `R2_S3_ENDPOINT`, or `MINIO_PUBLIC_ENDPOINT`
overrides the bundled MinIO origin; if `DATA_CENTRE_MEDIA_API_URL`,
`FRESH_DATA_CENTRE_INTERNAL_TOKEN`, or `MEDIA_DATABASE_URL` configures the
separate B2/R2 media service; or if the Federation API points to an external PostgreSQL host or a separate
`federation-db` container is detected. A Federation database on the existing
`postgres`/`federation-db` network alias is included because every connectable
non-template database in that PostgreSQL cluster is dumped. The helper also
fails closed if it finds non-system PostgreSQL roles beyond `postgres` and the
configured `POSTGRES_USER`, or user-defined PostgreSQL tablespaces; those
need explicit globals/tablespace backup and restore paths. When the standard
Data Centre Compose project is present, the helper discovers the
`video`/`video-worker` `/media` bind mount and includes that directory, plus
sibling `nodes` storage when present. Do not disable the guards or declare
recovery complete until every active external media store and separate database
is covered. The supported local-MinIO path includes all four buckets and any
detected local Data Centre storage. The free plan has a fixed 20 GiB machine disk and includes
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
ports, restores every database listed in the manifest, and checks that the
primary application database contains tables. It confirms that all four bucket
trees and any Data Centre storage directories
recorded in the backup manifest were restored, reports their file counts, and
securely removes its temporary directory, container, and volume on exit. The
script checks staging space before downloading and checks Docker storage against
the recorded raw PostgreSQL size before creating the isolated database volume.

This automated check does **not** restore media files into a running MinIO
instance, test public/private URLs, or measure production traffic recovery. Use
the full manual drill below before cutover and periodically thereafter.

1. Provision a clean, isolated recovery host and install compatible Docker,
   PostgreSQL client tools, MinIO client, and restic.
2. Configure `RESTIC_REPOSITORY` and `RESTIC_PASSWORD_FILE` for the backup
   repository, then run `./scripts/self-hosted-backup.sh check`.
3. List snapshots with `restic snapshots --host testagram-self-hosted --tag testagram`, select a known
   snapshot ID, and restore it to an empty recovery directory:
   `restic restore <snapshot-id> --target /secure/restore`.
4. Inspect the restored manifest and database inventory. Locate them with
   `find /secure/restore -name manifest.txt -o -name postgres-databases.txt`.
   The inventory lists every database and each archive is named
   `postgres-<database>.dump`; validate every archive with
   `pg_restore --list /path/to/postgres-<database>.dump`.
5. Start a disposable PostgreSQL instance with no public ports, using the
   `postgres_user` and `postgres_primary_database` values from the manifest.
   For each name in `postgres-databases.txt`, create the database if it is not
   the primary database or the built-in `postgres` database, then restore its
   matching archive with `pg_restore --no-owner --no-acl --dbname="$database"`.
   Review the target and permissions before executing; never point this command
   at the live production database during a drill.
6. Restore all four bucket contents (private media, public media, identity
   evidence, and metadata) into empty test buckets using `mc mirror` from the
   recovered directories. If the manifest records Data Centre video or node
   storage, restore those directories to the matching isolated storage paths.
   Verify object counts and sample hashes against the source where feasible.
7. Run application health checks and media-read checks against the isolated
   recovery stack. Record duration, missing data, recovery point (RPO), recovery
   time (RTO), and corrective actions in the operations log.
8. Securely remove plaintext restore material after sign-off and preserve the
   drill record. Repeat after schema, object-store, or backup-process changes.

## Scope and operational limits

- The backup includes every connectable non-template database in the
  configured PostgreSQL cluster, all four MinIO buckets, and detected local
  Data Centre video/node storage. It does not back up Docker images, host
  configuration, DNS, secrets, TLS private keys, or Valkey queue state.
- Back up the deployment configuration and secrets separately in a secure
  password manager/off-host secret store. Never commit real secrets.
- The script does not create an off-site repository for you, provision backup
  credentials, configure alerts, or prove a successful restore automatically.
- Run a restore drill before production cutover and periodically thereafter.
  A successful backup command is not proof of recoverability.
