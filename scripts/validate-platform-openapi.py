#!/usr/bin/env python3
"""Validate Testagram internal and hosting OpenAPI contracts without network access."""
from pathlib import Path

try:
    import yaml
except ImportError as exc:
    raise SystemExit("PyYAML is required: python -m pip install PyYAML") from exc

contracts = [
    (
        Path("docs/platform/openapi/internal-platform.yaml"),
        "Testagram Internal Platform API",
        {
            "/healthz", "/readyz", "/metrics",
            "/v1/cache/get", "/v1/cache/set", "/v1/cache/delete",
            "/v1/rate-limit/check", "/v1/queue/enqueue", "/v1/queue/claim",
            "/v1/queue/ack", "/v1/queue/reclaim", "/v1/config/set",
            "/v1/config/{key}", "/v1/events", "/v1/audit",
        },
    ),
    (
        Path("docs/platform/openapi/hosting-control-plane.yaml"),
        "Testagram Hosting Control Plane API",
        {
            "/healthz", "/readyz", "/v1/hosting/health",
            "/v1/hosting/nodes", "/v1/hosting/usage",
            "/v1/hosting/domains", "/v1/hosting/domains/{slug}/activate",
            "/v1/hosting/domains/{slug}/disable", "/v1/hosting/tls/authorize", "/v1/hosting/dns/records",
            "/v1/hosting/deployments", "/v1/hosting/deployments/{id}",
            "/v1/hosting/backups", "/v1/hosting/backups/{id}/verify",
            "/v1/hosting/recovery/drills",
        },
    ),
]

for path, expected_title, required in contracts:
    doc = yaml.safe_load(path.read_text(encoding="utf-8"))
    assert doc.get("openapi", "").startswith("3.1."), f"{path}: OpenAPI 3.1 is required"
    assert doc.get("info", {}).get("title") == expected_title, f"{path}: unexpected title"
    paths = doc.get("paths", {})
    missing = sorted(required - set(paths))
    assert not missing, f"{path}: missing endpoints: {missing}"
    for route, operations in paths.items():
        assert isinstance(operations, dict) and operations, f"{path}: no operations for {route}"
        for method, operation in operations.items():
            assert method in {"get", "post", "put", "patch", "delete"}, f"{path}: invalid method {method} on {route}"
            assert isinstance(operation.get("responses"), dict) and operation["responses"], f"{path}: missing responses for {method.upper()} {route}"
            if route.startswith("/v1/") and route not in {"/v1/config/{key}", "/v1/hosting/tls/authorize"}:
                assert operation.get("security", doc.get("security")), f"{path}: internal route lacks declared auth: {method.upper()} {route}"
    print(f"OpenAPI structure validation passed: {path} ({len(paths)} paths)")
