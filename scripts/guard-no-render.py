#!/usr/bin/env python3
"""Fail CI if retired hosting is reintroduced into operational configuration."""
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
FORBIDDEN_PATHS = {"render.yaml", "render.yml", "render.toml", "render.json", ".render"}
FORBIDDEN = re.compile(
    r"render\\.com|render\\.yaml|render\\.yml|render deploy|render blueprint|"
    r"RENDER_API_KEY|RENDER_SERVICE_ID|RENDER_DEPLOY_HOOK",
    re.IGNORECASE,
)
OPERATIONAL_ROOTS = (".github/workflows", "deploy", "infra", "infrastructure", "scripts")
OPERATIONAL_SUFFIXES = {".yml", ".yaml", ".toml", ".json", ".tf", ".sh", ".ps1"}
EXEMPT = {
    "scripts/guard-no-render.py",
    "docs/architecture/instabox-only-deployment.md",
    "docs/six-repository-runtime-integration-contract.md",
    "README.md",
}

tracked = subprocess.run(
    ["git", "ls-files", "-z"], cwd=ROOT, check=True, capture_output=True
).stdout.decode().split("\\0")
violations = []
for raw in filter(None, tracked):
    path = Path(raw)
    normalized = path.as_posix()
    if normalized in FORBIDDEN_PATHS or any(
        normalized.startswith(item + "/") for item in FORBIDDEN_PATHS
    ):
        violations.append(f"{normalized}: retired deployment artifact")
        continue
    if normalized in EXEMPT:
        continue
    in_operational_root = any(
        normalized == root or normalized.startswith(root + "/")
        for root in OPERATIONAL_ROOTS
    )
    root_deploy_metadata = path.parent == Path(".") and (
        normalized.startswith("docker-compose")
        or normalized.startswith("compose.")
        or normalized.startswith("Dockerfile")\n        or normalized in {"Makefile"}
    )
    if not (in_operational_root or root_deploy_metadata):
        continue
    if path.suffix.lower() not in OPERATIONAL_SUFFIXES and not root_deploy_metadata:
        continue
    try:
        content = (ROOT / path).read_text(encoding="utf-8")
    except (UnicodeDecodeError, OSError):
        continue
    for number, line in enumerate(content.splitlines(), 1):
        if FORBIDDEN.search(line):
            violations.append(f"{normalized}:{number}: retired hosting reference")

if violations:
    print("Retired hosting configuration detected:")
    print("\\n".join(f" - {item}" for item in violations))
    sys.exit(1)
print("Instabox-only deployment guard passed; no retired hosting artifacts found.")
