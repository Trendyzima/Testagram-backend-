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

    def test_deployer_normalizes_boolean_flags_before_fail_closed_validation(self):
        self.assertIn("normalize_boolean FRESH_MEDIA_ENABLED false", self.deployer)
        self.assertIn("normalize_boolean DATA_CENTRE_MEDIA_ENABLED false", self.deployer)
        self.assertIn("normalize_boolean MEDIA_REPLICATION_ENABLED false", self.deployer)
        self.assertIn("tr -d", self.deployer)
        self.assertIn("surrounding whitespace is ignored", self.deployer)
        self.assertIn("refusing an ambiguous media profile", self.deployer)

    def test_boolean_normalizer_accepts_whitespace_and_rejects_invalid_values(self):
        match = re.search(r"normalize_boolean\\(\\) \\{.*?\\n\\}", self.deployer, re.S)
        self.assertIsNotNone(match)
        helper = match.group(0)
        accepted = helper + r'''
FRESH_MEDIA_ENABLED=
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
 \\ttrue \\r\\n'
normalize_boolean FRESH_MEDIA_ENABLED false
printf '%s\\n' "$FRESH_MEDIA_ENABLED"
'''
        result = subprocess.run(["bash", "-c", accepted], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), "true")
        rejected = helper + '''
FRESH_MEDIA_ENABLED=tru
normalize_boolean FRESH_MEDIA_ENABLED false
'''
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
