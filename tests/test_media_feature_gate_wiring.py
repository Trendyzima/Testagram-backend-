#!/usr/bin/env python3
"""Regression tests for explicit Testagram media deployment approvals."""
from pathlib import Path
import re
import subprocess
import unittest

ROOT = Path(__file__).resolve().parents[1]


class MediaFeatureGateWiringTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.workflow = (ROOT / ".github/workflows/instabox-deploy.yml").read_text()
        cls.deployer = (ROOT / "scripts/start-instabox-deployment.sh").read_text()

    def test_workflow_passes_all_three_repository_gates(self):
        for key in (
            "DATA_CENTRE_MEDIA_ENABLED",
            "FRESH_MEDIA_ENABLED",
            "MEDIA_REPLICATION_ENABLED",
        ):
            with self.subTest(key=key):
                self.assertIn(f"vars.{key} || 'false'", self.workflow)

    def test_identity_registration_requires_the_private_data_centre_evidence_profile(self):
        self.assertIn("vars.DATA_CENTRE_MEDIA_ENABLED == 'true'", self.workflow)
        self.assertIn("vars.FRESH_MEDIA_ENABLED == 'true'", self.workflow)
        self.assertIn("inputs.confirm_identity_registration == true", self.workflow)
        self.assertNotIn("vars.MEDIA_REPLICATION_ENABLED == 'true' && inputs.confirm_identity_registration", self.workflow)
        self.assertIn("Identity registration requires the private Data Centre media profile", self.deployer)
        self.assertIn('ensure_random_secret IDENTITY_EVIDENCE_DATA_CENTRE_INTERNAL_TOKEN', self.deployer)
        self.assertIn('write_env_value IDENTITY_EVIDENCE_DATA_CENTRE_INTERNAL_TOKEN "$IDENTITY_EVIDENCE_DATA_CENTRE_INTERNAL_TOKEN"', self.deployer)
        self.assertIn('write_dc_env_value IDENTITY_EVIDENCE_DATA_CENTRE_INTERNAL_TOKEN "$IDENTITY_EVIDENCE_DATA_CENTRE_INTERNAL_TOKEN"', self.deployer)
        self.assertIn('write_env_value IDENTITY_EVIDENCE_DATA_CENTRE_INTERNAL_TOKEN ""', self.deployer)
        self.assertIn('write_dc_env_value IDENTITY_EVIDENCE_DATA_CENTRE_INTERNAL_TOKEN ""', self.deployer)

    def test_native_jwt_and_session_database_are_shared_with_data_centre_consumers(self):
        self.assertIn('write_dc_env_value TESTAGRAM_AUTH_JWT_SECRET "$TESTAGRAM_AUTH_JWT_SECRET"', self.deployer)
        self.assertIn('write_dc_env_value DATABASE_URL "$shared_database_url"', self.deployer)
        self.assertIn('verify_container_env "$BASE_ENV" "$BASE_COMPOSE" api TESTAGRAM_AUTH_JWT_SECRET "$TESTAGRAM_AUTH_JWT_SECRET"', self.deployer)
        self.assertIn('verify_container_env "$DC_ENV" "$DATACENTRE/backend/docker-compose.yml" video TESTAGRAM_AUTH_JWT_SECRET "$TESTAGRAM_AUTH_JWT_SECRET"', self.deployer)
        self.assertIn('verify_container_env "$DC_ENV" "$DATACENTRE/backend/docker-compose.yml" notification TESTAGRAM_AUTH_JWT_SECRET "$TESTAGRAM_AUTH_JWT_SECRET"', self.deployer)
        self.assertIn('verify_container_env "$DC_ENV" "$DATACENTRE/backend/docker-compose.yml" video DATABASE_URL "$shared_database_url"', self.deployer)
        self.assertIn('verify_container_env "$DC_ENV" "$DATACENTRE/backend/docker-compose.yml" notification DATABASE_URL "$shared_database_url"', self.deployer)

    def test_identity_evidence_token_is_passed_to_the_private_backend_service(self):
        compose = (ROOT / "deploy/self-hosted/docker-compose.yml").read_text()
        self.assertIn("IDENTITY_EVIDENCE_DATA_CENTRE_INTERNAL_TOKEN: ${IDENTITY_EVIDENCE_DATA_CENTRE_INTERNAL_TOKEN:-}", compose)
    def test_provider_smoke_secret_sources_and_retired_identity_smoke(self):
        for key in ("B2_S3_ENDPOINT", "B2_S3_REGION"):
            with self.subTest(key=key):
                self.assertIn(f"{key}: ${{{{ secrets.{key} }}}}", self.workflow)
                self.assertNotIn(f"{key}: ${{{{ vars.{key} }}}}", self.workflow)

        identity_smoke = (
            ROOT / ".github/workflows/identity-evidence-staging-smoke.yml"
        ).read_text()
        media_smoke = (
            ROOT / ".github/workflows/media-provider-smoke.yml"
        ).read_text()
        self.assertIn("Retired", identity_smoke)
        self.assertIn("Testagram-data-centre-/.github/workflows/media-provider-smoke.yml", identity_smoke)
        self.assertNotIn("IDENTITY_SMOKE_B2_ENDPOINT", identity_smoke)
        self.assertNotIn("IDENTITY_SMOKE_R2_ENDPOINT", identity_smoke)
        self.assertIn("B2_ENDPOINT: ${{ secrets.B2_S3_ENDPOINT }}", media_smoke)
        self.assertIn("B2_REGION: ${{ secrets.B2_S3_REGION }}", media_smoke)

    def test_deployer_normalizes_boolean_flags_before_fail_closed_validation(self):
        self.assertIn("normalize_boolean FRESH_MEDIA_ENABLED false", self.deployer)
        self.assertIn("normalize_boolean DATA_CENTRE_MEDIA_ENABLED false", self.deployer)
        self.assertIn("normalize_boolean MEDIA_REPLICATION_ENABLED false", self.deployer)
        self.assertIn("tr -d", self.deployer)
        self.assertIn("surrounding whitespace is ignored", self.deployer)
        self.assertIn("refusing an ambiguous media profile", self.deployer)

    def test_boolean_normalizer_accepts_whitespace_and_rejects_invalid_values(self):
        match = re.search(r"normalize_boolean\(\) \{.*?\n\}", self.deployer, re.S)
        self.assertIsNotNone(match)
        helper = match.group(0)
        accepted = helper + "\n" + "\n".join([
            "FRESH_MEDIA_ENABLED=$' \\ttrue \\r\\n'",
            "normalize_boolean FRESH_MEDIA_ENABLED false",
            "printf '%s\\n' \"$FRESH_MEDIA_ENABLED\"",
        ]) + "\n"
        result = subprocess.run(["bash", "-c", accepted], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), "true")

        rejected = helper + "\n" + "\n".join([
            "FRESH_MEDIA_ENABLED=tru",
            "normalize_boolean FRESH_MEDIA_ENABLED false",
        ]) + "\n"
        result = subprocess.run(["bash", "-c", rejected], capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)

    def test_fresh_media_approval_reaches_remote_deployment(self):
        self.assertIn('--arg fresh_media_enabled "$FRESH_MEDIA_ENABLED"', self.deployer)
        self.assertIn("FRESH_MEDIA_ENABLED:$fresh_media_enabled", self.deployer)
        self.assertIn('write_env_value FRESH_MEDIA_ENABLED "$FRESH_MEDIA_ENABLED"', self.deployer)
        self.assertNotIn('write_env_value FRESH_MEDIA_ENABLED "$media_enabled"', self.deployer)

    def test_pushes_cannot_implicitly_deploy_to_production(self):
        deploy_gate = self.workflow.split("  deploy:", 1)[1].split("    name:", 1)[0]
        self.assertIn("github.event_name == 'workflow_dispatch'", deploy_gate)
        self.assertIn("inputs.confirm_production_deploy == true", deploy_gate)
        self.assertNotIn("github.event_name == 'push' ||", deploy_gate)


if __name__ == "__main__":
    unittest.main()
