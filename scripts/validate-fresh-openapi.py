#!/usr/bin/env python3
from pathlib import Path
import sys

try:
    import yaml
except ImportError:
    print("PyYAML is required to validate the fresh API OpenAPI contract", file=sys.stderr)
    raise

spec = yaml.safe_load(Path("docs/fresh-platform-api.openapi.yaml").read_text())
assert spec.get("openapi") == "3.1.0", "OpenAPI version must be 3.1.0"
paths = spec.get("paths", {})
required = {
    "/healthz": {"get"},
    "/readyz": {"get"},
    "/v1/me": {"get"},
    "/v1/me/profile": {"put"},
    "/v1/feed/home": {"get"},
    "/v1/posts": {"post"},
    "/v1/media/uploads/reservations": {"post"},
    "/v1/media/uploads/{reservationID}/complete": {"post"},
    "/v1/media/{objectID}/download": {"get"},
    "/v1/media/{objectID}/restore": {"post"},
}
for path, methods in required.items():
    assert path in paths, f"missing OpenAPI path: {path}"
    for method in methods:
        assert method in paths[path], f"missing OpenAPI operation: {method.upper()} {path}"
assert "/internal/v1/feed/rank" not in paths, "Algorithm service route must not be advertised as a Backend route"
print("Fresh Backend OpenAPI contract: PASS")
