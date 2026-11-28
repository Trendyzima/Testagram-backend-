#!/usr/bin/env python3
"""Regression tests for independent core deployment and private-media activation gates."""
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = (ROOT / "scripts/start-instabox-deployment.sh").read_text()
WORKFLOW = (ROOT / ".github/workflows/instabox-deploy.yml").read_text()


class CoreDeploymentMediaGateTests(unittest.TestCase):
    def test_media_defaults_off_and_mismatched_flags_still_fail_closed(self):
        self.assertIn("normalize_boolean FRESH_MEDIA_ENABLED false", SCRIPT)
        self.assertIn("normalize_boolean DATA_CENTRE_MEDIA_ENABLED false", SCRIPT)
        self.assertIn('if [[ "${FRESH_MEDIA_ENABLED:-false}" != "${DATA_CENTRE_MEDIA_ENABLED:-false}" ]]', SCRIPT)

    def test_media_off_does_not_abort_core_deployment(self):
        self.assertIn("Core service deployment may continue.", SCRIPT)
        self.assertNotIn("Identity registration requires the private Data Centre media profile", SCRIPT)

    def test_registration_remains_closed_without_private_media(self):
        self.assertIn("identity registration remains fail-closed", SCRIPT)
        self.assertIn('DC_MEDIA_PROFILE_ENABLED="${DATA_CENTRE_MEDIA_ENABLED:-false}"', SCRIPT)

    def test_provider_provisioning_is_conditional_on_both_explicit_gates(self):
        condition = "if: steps.media-gates.outputs.media_enabled == 'true'"
        self.assertEqual(WORKFLOW.count(condition), 2)
        self.assertIn('echo "media_enabled=$early_dc_media_enabled" >> "$GITHUB_OUTPUT"', WORKFLOW)
        self.assertIn("Internal workflow error: R2 provisioning ran without the Data Centre service gate.", WORKFLOW)
        self.assertIn("Internal workflow error: B2 provisioning ran without the Data Centre service gate.", WORKFLOW)

    def test_workflow_validation_accepts_core_only_mode(self):
        self.assertIn("Validate independent production media feature gates", WORKFLOW)
        self.assertIn("Core-only deployment selected.", WORKFLOW)
        self.assertNotIn("Production media gates are not enabled.", WORKFLOW)

    def test_ci_runs_this_regression_suite(self):
        self.assertIn("python3 tests/test_core_deploy_media_gate.py -v", WORKFLOW)


if __name__ == "__main__":
    unittest.main()
