#!/usr/bin/env python3
"""Prevent retired external identity-provider traces from re-entering runtime/configuration."""
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[1]
RUNTIME_ROOTS = (
    "cmd",
    "internal",
    "admin-console",
    "deploy",
    "vps",
    "docs",
    ".github/workflows",
    "scripts",
)
RUNTIME_FILES = (
    ".env.example",
    "go.mod",
    "go.sum",
    "Dockerfile",
    "scripts/native-auth-staging-smoke.py",
    "scripts/validate-native-api-contract.py",
)
HISTORICAL_MIGRATION_FIXTURE_FILES = {"scripts/test-identity-registration-migration.sh"}
TEXT_SUFFIXES = {
    ".conf", ".go", ".json", ".md", ".mod", ".py", ".service", ".sh",
    ".sum", ".toml", ".txt", ".yaml", ".yml",
}


class NativeAuthRuntimeBoundaryTests(unittest.TestCase):
    def test_legacy_provider_names_are_confined_to_ci_migration_fixtures(self):
        # This script deliberately seeds the pre-retirement PostgreSQL schema so
        # forward migrations are tested against real historical identifiers.
        # It is not a runtime, deployment, or operator migration command.
        fixture = "scripts/test-identity-registration-migration.sh"
        self.assertEqual(HISTORICAL_MIGRATION_FIXTURE_FILES, {fixture})
        self.assertTrue((ROOT / fixture).is_file())

        ci_workflow = (ROOT / ".github" / "workflows" / "ci.yml").read_text(encoding="utf-8")
        self.assertIn(f"bash {fixture}", ci_workflow)

        deployment_roots = (
            ROOT / "deploy",
            ROOT / "vps",
            ROOT / "scripts" / "start-instabox-deployment.sh",
            ROOT / "deploy" / "self-hosted" / "apply-native-identity-migrations.sh",
        )
        for target in deployment_roots:
            if target.is_file():
                content = target.read_text(encoding="utf-8")
                self.assertNotIn(fixture, content, f"{target.relative_to(ROOT)} must not invoke a test fixture")
            elif target.is_dir():
                for path in target.rglob("*"):
                    if path.is_file() and path.suffix.lower() in TEXT_SUFFIXES:
                        content = path.read_text(encoding="utf-8", errors="ignore")
                        self.assertNotIn(fixture, content, f"{path.relative_to(ROOT)} must not invoke a test fixture")

    def test_staging_auth_smoke_checks_public_signup_is_independent_of_identity_review(self):
        smoke = (ROOT / "scripts/native-auth-staging-smoke.py").read_text(encoding="utf-8")
        self.assertIn('require_status(base, "/api/me/profile", 401)', smoke)
        self.assertIn('"/api/auth/signup", 400, "INVALID_PASSWORD"', smoke)
        self.assertIn('independently of identity review', smoke)

    def test_identity_review_is_not_a_deployment_or_signup_prerequisite(self):
        workflow = (ROOT / ".github" / "workflows" / "instabox-deploy.yml").read_text(encoding="utf-8")
        compose = (ROOT / "deploy" / "self-hosted" / "docker-compose.yml").read_text(encoding="utf-8")
        acceptance = (ROOT / "docs" / "platform" / "native-auth-acceptance.md").read_text(encoding="utf-8")
        self.assertIn("vars.TESTAGRAM_IDENTITY_REVIEW_WEBAUTHN_ENABLED || 'false'", workflow)
        self.assertIn('if [[ "$TESTAGRAM_IDENTITY_REVIEW_WEBAUTHN_ENABLED" == "true" ]]; then', workflow)
        self.assertIn("Optional identity-evidence intake gate only", compose)
        self.assertIn("does not call identity-application intake", acceptance)
        self.assertNotIn("intentionally fail-closed and returns", acceptance)
        self.assertNotIn("public signup remains fail-closed", acceptance.lower())

    def test_staging_auth_smoke_checks_logout_revokes_refresh_credentials(self):
        smoke = (ROOT / "scripts/native-auth-staging-smoke.py").read_text(encoding="utf-8")
        self.assertIn('body={"refresh_token": second["refresh_token"]}', smoke)
        self.assertIn('body={"refresh_token": third["refresh_token"]}', smoke)
        self.assertIn("logout revokes both access and refresh credentials", smoke)
        self.assertIn("logout-all revokes access and refresh credentials across the account", smoke)

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
            if not path.is_file() or str(path.relative_to(ROOT)) in HISTORICAL_MIGRATION_FIXTURE_FILES:
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
