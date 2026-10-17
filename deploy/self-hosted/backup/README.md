# Encrypted backup and recovery

These scripts use Restic client-side encryption for the PostgreSQL archive and a filesystem mirror of both MinIO buckets. Restic writes to a separately configured S3-compatible repository, so the backup can live on another machine or in a separate failure domain.

## Configure before running

In deploy/self-hosted/.env, replace the backup placeholders:

- BACKUP_RESTIC_REPOSITORY: a dedicated repository such as s3:https://backup.example.com/testagram-backups/testagram-prod. Use an HTTPS endpoint and isolate repositories per environment.
- BACKUP_RESTIC_PASSWORD: a strong unique password. Store it separately in a password manager/offline recovery kit; without it, Restic snapshots cannot be decrypted.
- BACKUP_S3_ACCESS_KEY and BACKUP_S3_SECRET_KEY: least-privilege credentials limited to the repository's bucket/prefix.

The destination should enable versioning or immutability where supported. Keep the Restic password and S3 credentials out of Git, logs, client applications and the primary host's only backup copy. This backs up the PostgreSQL database and current objects from the private/public MinIO buckets. It does not back up the secret values themselves; preserve those separately and securely.

## Create and verify a backup

From deploy/self-hosted:

```sh
bash backup/backup.sh
```

The command prints a backup ID, database SHA-256 and Restic repository confirmation. The snapshot contains a manifest, a PostgreSQL custom-format dump, and filesystem copies of both media buckets. Restic encrypts and authenticates the complete snapshot before upload.

## Perform a restore drill

Use a backup ID printed by the backup command:

```sh
bash backup/restore-smoke.sh 20261017T120000Z-1234
```

The restore smoke test downloads and decrypts the selected Restic snapshot, checks the PostgreSQL archive checksum, restores the database into a disposable PostgreSQL 17 container with no published ports, then rehydrates private and public objects into a disposable MinIO container and compares the restored objects. Temporary containers, network and decrypted files are removed on exit.

Run this on a schedule and alert if it fails or the newest backup is too old. These scripts do not change production DNS or replace the need for an off-host copy, a documented RPO/RTO, credential recovery, and periodic manual review.
