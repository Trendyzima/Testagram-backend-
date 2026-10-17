# Backup automation requirements

Do not enable a backup job with placeholder credentials. Configure separate
least-privilege database and object-store credentials and encrypted off-host
backup storage first.

- PostgreSQL: scheduled pg_dump -Fc, encrypt before transfer, verify exit
  status and retention, and test restore into a clean database.
- Object store: enable versioning/replication or scheduled mirror to a
  separate machine; validate object counts and checksums.
- Configuration: back up non-secret configuration and store secrets separately
  in an offline encrypted secret manager.
- Monitoring: alert on last-success timestamp, backup age, free disk space,
  and restore-test failure.
- Recovery: document RPO/RTO, run restore drills, and keep one copy outside
  the primary host and power/network failure domain.

Never treat a second disk in the same machine as the only backup.
