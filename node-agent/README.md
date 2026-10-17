# Testagram node agent

The node agent is a **worker for bounded, one-shot container workloads**. It is not yet a long-running web-app deployment manager: service readiness checks, traffic switching, and automatic release rollback remain separate release gates.

## Trust boundary

- Run this process only on a dedicated worker you control. It invokes the local Docker/Podman CLI; access to the container runtime is effectively host-level privilege.
- Do not mount the Docker socket into a tenant-facing or untrusted application container.
- Use HTTPS for the control plane. Plain HTTP is accepted only for loopback development.
- The bootstrap token is used only to enroll a worker; the returned node token is stored in a state file with owner-only permissions. Keep the state file and bootstrap token out of logs and source control.
- Workloads must use an immutable lowercase SHA-256 image digest. The worker applies CPU/memory limits, a read-only root filesystem, dropped Linux capabilities, a PID limit, no-new-privileges, a constrained temporary filesystem, and a bounded execution timeout.
- Workloads currently use Docker's bridge network. Do not schedule untrusted tenant builds here; build isolation requires a separate sandboxed builder pool with strict egress controls.

## Build and run on a worker

Install Go 1.26+, Docker Engine or Podman, and a system account that has only the permissions needed for the selected runtime. Build from the repository root:

~~~sh
go test -race -count=1 ./node-agent
go vet ./node-agent
CGO_ENABLED=0 go build -trimpath -o testagram-node-agent ./node-agent
sudo install -o root -g root -m 0755 testagram-node-agent /usr/local/bin/testagram-node-agent
sudo install -d -o root -g root -m 0700 /etc/testagram
sudo install -d -o testagram-node -g testagram-node -m 0700 /var/lib/testagram/node-agent
~~~

Create /etc/testagram/node-agent.env as root with mode 0600:

~~~sh
VPS_CONTROL_PLANE_URL=https://control.example.test
VPS_NODE_BOOTSTRAP_TOKEN=replace-with-a-unique-long-random-token
VPS_NODE_NAME=worker-01
VPS_VOLUME_ROOT=/var/lib/testagram/storage/nodes
VPS_NODE_STATE_FILE=/var/lib/testagram/node-agent/node-state.json
VPS_WORKLOAD_TIMEOUT=30m
~~~

The control-plane address must be reachable from the worker. The control-plane service must be reachable at the /v1/nodes/register, /v1/nodes/heartbeat, /v1/nodes/poll, and /v1/workloads/{id}/result endpoints. Use the bootstrap token configured on the control-plane and never place the internal operator token on the worker.

A hardened systemd unit can be created at /etc/systemd/system/testagram-node-agent.service:

~~~ini
[Unit]
Description=Testagram workload worker
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=testagram-node
Group=testagram-node
EnvironmentFile=/etc/testagram/node-agent.env
ExecStart=/usr/local/bin/testagram-node-agent
Restart=on-failure
RestartSec=5s
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/testagram
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
LockPersonality=true
RestrictSUIDSGID=true

[Install]
WantedBy=multi-user.target
~~~

**Runtime permissions need a deliberate decision:** Docker commonly requires root or membership in the docker group, which is effectively root-equivalent. Do not add the account to that group on a shared or tenant-facing host without accepting that risk. Prefer a dedicated worker VM/host and an appropriately restricted runtime design.

Then enable the service:

~~~sh
sudo chmod 600 /etc/testagram/node-agent.env
sudo systemctl daemon-reload
sudo systemctl enable --now testagram-node-agent
sudo journalctl -u testagram-node-agent -f
~~~

## Release gates still required

Before production scheduling, add an end-to-end test using an isolated worker and test database; lease recovery for jobs abandoned after worker loss; explicit cancellation; signed or otherwise authenticated workload envelopes; a per-tenant quota model; log/exit-code capture; and a separate persistent-service deployment protocol with readiness checks and rollback. This worker intentionally does not claim those capabilities.
