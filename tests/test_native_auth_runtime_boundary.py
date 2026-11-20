#!/usr/bin/env python3
"""Prevent retired external identity-provider traces from re-entering runtime/configuration."""
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[1]
RUNTIME_ROOTS = (
    "cmd",
    "internal",
    "admin-console",
    "deploy/self-hosted",
    ".github/workflows",
)
RUNTIME_FILES = (
    ".env.example",
    "go.mod",
    "go.sum",
    "Dockerfile",
    "scripts/native-auth-staging-smoke.py",
    "scripts/validate-native-api-contract.py",
)
TEXT_SUFFIXES = {
    ".conf", ".go", ".json", ".md", ".mod", ".py", ".service", ".sh",
    ".sum", ".toml", ".txt", ".yaml", ".yml",
}


class NativeAuthRuntimeBoundaryTests(unittest.TestCase):
    def test_retired_provider_is_absent_from_runtime_and_deployment_inputs(self):
        paths = [ROOT / name for name in RUNTIME_FILES]
        for root_name in RUNTIME_ROOTS:
            root = ROOT / root_name
            if root.exists():
                paths.extend(
                    path for path in root.rglob("*")
                    if path.is_file() and path.suffix.lower() in TEXT_SUFFIXES
                )

        offenders = []
        for path in sorted(set(paths)):
            if not path.is_file():
                continue
            try:
                content = path.read_text(encoding="utf-8")
            except UnicodeDecodeError:
                continue
            for line_number, line in enumerate(content.splitlines(), start=1):
                if "appwrite" in line.lower():
                    offenders.append(f"{path.relative_to(ROOT)}:{line_number}: {line.strip()}")

        self.assertEqual(
            offenders,
            [],
            "Retired provider references must stay out of Testagram runtime/configuration:\n"
            + "\n".join(offenders),
        )


if __name__ == "__main__":
    unittest.main()
