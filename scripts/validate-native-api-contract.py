#!/usr/bin/env python3
"""Guard the production API against retired identity-provider runtime dependencies."""
from pathlib import Path
import re
import sys

ROOT = Path(__file__).resolve().parents[1]
RUNTIME_ROOTS = (ROOT / "cmd", ROOT / "internal", ROOT / "admin-console", ROOT / "control-plane", ROOT / "deploy" / "self-hosted", ROOT / ".github" / "workflows")
ROOT_FILES = (ROOT / "docker-compose.yml", ROOT / "Dockerfile", ROOT / ".env.example")
FORBIDDEN = re.compile(
    r"EXTERNAL_IDENTITY_PROVIDER_(?:URL|KEY|PROJECT_ID)|"
    r"IDENTITY_PROVIDER_(?:ENDPOINT|PROJECT_ID|API_KEY)|"
    r"X-External-Identity-Provider|external_auth_fallback",
    re.IGNORECASE,
)

failures = []
scanned = 0
for base in RUNTIME_ROOTS:
    if not base.exists():
        continue
    for path in base.rglob("*"):
        if not path.is_file() or path.suffix.lower() not in {
            ".go", ".yml", ".yaml", ".json", ".sh", ".py", ".js", ".ts", ".tsx", ".html", ".env", ".conf", ".toml"
        }:
            continue
        scanned += 1
        try:
            content = path.read_text(encoding="utf-8")
        except UnicodeDecodeError:
            continue
        for line_number, line in enumerate(content.splitlines(), 1):
            if FORBIDDEN.search(line):
                failures.append(f"{path.relative_to(ROOT)}:{line_number}: retired identity-provider trace")

for path in ROOT_FILES:
    if not path.exists():
        continue
    scanned += 1
    for line_number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        if FORBIDDEN.search(line):
            failures.append(f"{path.relative_to(ROOT)}:{line_number}: retired identity-provider trace")

router = (ROOT / "cmd" / "api" / "main.go").read_text(encoding="utf-8")
required_routes = (
    'mux.HandleFunc("POST /api/identity/applications"',
    'mux.HandleFunc("GET /api/me/profile"',
    'mux.HandleFunc("PUT /api/me/profile"',
    'mux.HandleFunc("POST /api/auth/"+action',
)
for route in required_routes:
    if route not in router:
        failures.append(f"cmd/api/main.go: missing native route registration {route}")

for action in ('"signup"', '"login"', '"refresh"', '"logout"'):
    if action not in router:
        failures.append(f"cmd/api/main.go: missing native authentication action {action}")

if failures:
    print("Native API contract validation failed:", file=sys.stderr)
    print("\n".join(failures), file=sys.stderr)
    raise SystemExit(1)

print(f"Native Testagram API contract: PASS ({scanned} runtime/config files scanned)")
