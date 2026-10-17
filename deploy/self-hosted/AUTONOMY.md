# Testagram VPS autonomous recovery

This installs a **bounded service reconciler**, not an unbreakable anti-hacker system. It recovers ordinary service failures and transient network/Docker outages without requiring an operator to SSH in for every restart.

## Behavior

- Existing Docker `restart: unless-stopped` policies restart application containers after process failure and Docker daemon/host restarts.
- A root-owned systemd timer runs shortly after boot and every 60 seconds thereafter. It checks Docker/Compose/configuration and executes idempotent `docker compose up -d --remove-orphans`.
- If the VPS boots without working network connectivity, the timer keeps retrying. Once the route/network and image registry are reachable, missing-image pulls and reconciliation can succeed on a later tick. Cached/local services are not intentionally held offline while waiting for Internet access.
- Containers with health checks that remain `unhealthy` may be restarted at most three times in a rolling 30-minute window. After that, the reconciler leaves the container for operator investigation and emits a journal message rather than restart-looping indefinitely.
- Logs: `journalctl -u testagram-reconcile.service`. Timer: `systemctl status testagram-reconcile.timer`.
- A lock prevents overlapping runs. The reconciler never runs `docker compose down -v`, deletes persistent data, prunes images, mutates source code, rotates credentials, or changes SSH/firewall rules.

## Install

1. Deploy this repository on the VPS and configure `deploy/self-hosted/.env`. Replace every `REPLACE_WITH_...` value with a unique random secret; never commit the real file. Confirm the Compose configuration:
   ```sh
   cd deploy/self-hosted
   chmod 600 .env
   docker compose config --quiet
   ```
2. From the repository root:
   ```sh
   sudo bash scripts/install-testagram-autonomy.sh
   ```
3. Verify:
   ```sh
   systemctl is-enabled testagram-reconcile.timer
   systemctl list-timers testagram-reconcile.timer
   systemctl status testagram-reconcile.service
   journalctl -u testagram-reconcile.service -n 100 --no-pager
   docker compose -f deploy/self-hosted/docker-compose.yml ps
   ```
4. Reboot-test during a maintenance window, then test network recovery using the hosting provider's console. Do not perform this test over your only SSH connection.

The installer enables the recovery timer. It does not install unattended OS upgrades, open ports, change SSH settings, or enable a firewall; those changes depend on the provider, current remote-access configuration and operating system. Apply a separately reviewed host-hardening baseline before production.

## Security and attack recovery

Automatic restart helps with process crashes, not a compromised host. An attacker with root or Docker-socket access can disable the reconciler or alter persistent data. Do not automatically execute untrusted repair commands, deploy unreviewed code, wipe volumes, or reboot repeatedly in response to suspicious traffic.

- Expose only TCP 80/443 publicly unless SSH is intentionally needed; never publish Postgres, Valkey, MinIO, Prometheus or the private control plane.
- Restrict SSH to keys and a trusted source/VPN where possible; preserve a tested provider-console recovery route before changing firewall policy.
- Install/configure a host intrusion and brute-force defense such as Fail2ban only after validating the SSH service, log backend and allowlist.
- Alert on repeated unhealthy restarts, container exits, disk exhaustion, unexpected login activity and backup failures; forward logs outside the VPS so an attacker cannot erase all evidence.
- Maintain encrypted off-host backups and periodically restore PostgreSQL and media to an isolated host. A single VPS cannot self-heal physical disk loss, provider outages, DDoS saturation, or destructive compromise.

## Validation

```sh
bash -n scripts/testagram-reconcile.sh
bash -n scripts/install-testagram-autonomy.sh
docker compose -f deploy/self-hosted/docker-compose.yml config --quiet
```

GitHub validates shell syntax and Compose rendering. It cannot prove the timer is installed on the live VPS, network recovery works, or the host can restore backups. Those require on-host drills.
