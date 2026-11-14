#!/usr/bin/env python3
"""Guard the public/private readiness contract between Caddy and deploy smoke."""

from pathlib import Path

caddy = Path("deploy/self-hosted/Caddyfile").read_text()
workflow = Path(".github/workflows/instabox-deploy.yml").read_text()

assert "@internal_observability path /metrics /readyz" in caddy
assert "respond @internal_observability 404" in caddy, (
    "Public readiness and metrics routes must remain hidden by Caddy"
)
assert "http://127.0.0.1:8080/readyz" in workflow, (
    "Deployment must verify Backend readiness from inside the container"
)

canonical_start = workflow.index('ready_status=000', workflow.index("CANONICAL_API_URL"))
canonical_end = workflow.index('exit 1', canonical_start)
canonical_smoke = workflow[canonical_start:canonical_end]

assert 'ready_status=="404"' in canonical_smoke, (
    "Canonical public smoke must expect the intentionally hidden /readyz route"
)
assert 'health_status" == "200"' in canonical_smoke
assert 'catalog_status" == "401"' in canonical_smoke
assert 'grep -qx \'ok\' "$tmp/health"' in canonical_smoke
assert 'grep -qx \'ready\' "$tmp/ready"' not in canonical_smoke, (
    "Do not require the private readiness body over the public hostname"
)
assert "Expected public /healthz=200, hidden /readyz=404" in canonical_smoke

print("Canonical deployment smoke contract passed: public liveness, private readiness and OIDC-protected catalogue are distinct.")
