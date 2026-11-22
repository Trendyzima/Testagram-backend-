#!/usr/bin/env python3
"""Regression tests for the Cloudflare-to-Caddy edge shared credential path."""

from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = ROOT / ".github" / "workflows" / "instabox-deploy.yml"
DEPLOY_SCRIPT = ROOT / "scripts" / "start-instabox-deployment.sh"


class EdgeProxySharedSecretTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.workflow = WORKFLOW.read_text(encoding="utf-8")
        cls.script = DEPLOY_SCRIPT.read_text(encoding="utf-8")

    def test_production_workflow_requires_and_passes_the_exact_secret(self):
        secret_ref = "TESTAGRAM_EDGE_PROXY_SHARED_TOKEN: ${{ secrets.TESTAGRAM_EDGE_PROXY_SHARED_TOKEN }}"
        self.assertIn(secret_ref, self.workflow)
        self.assertIn("Validate edge proxy shared credential", self.workflow)
        self.assertIn('[[ ! "${TESTAGRAM_EDGE_PROXY_SHARED_TOKEN:-}" =~ ^[A-Fa-f0-9]{64}$ ]]', self.workflow)
        self.assertIn('--arg edge_proxy_token "$TESTAGRAM_EDGE_PROXY_SHARED_TOKEN"', self.script)
        self.assertIn("EDGE_PROXY_SHARED_TOKEN:$edge_proxy_token", self.script)
        self.assertNotIn("ensure_random_secret EDGE_PROXY_SHARED_TOKEN", self.script)
        self.assertNotIn('EDGE_PROXY_SHARED_TOKEN="$(openssl rand', self.script)

    def test_production_media_gates_are_checked_before_machine_selection(self):
        gate_step = self.workflow.index("      - name: Verify production media feature gates")
        machine_step = self.workflow.index("      - name: Resolve and start the InstaBox machine")
        self.assertLess(gate_step, machine_step)
        gate = self.workflow[gate_step:machine_step]
        self.assertIn("vars.DATA_CENTRE_MEDIA_ENABLED || 'false'", gate)
        self.assertIn("vars.FRESH_MEDIA_ENABLED || 'false'", gate)
        self.assertIn('[[ "$early_dc_media_enabled" != "true" || "$early_fresh_gate" != "true" ]]', gate)
        self.assertIn("GitHub production environment", gate)

    def test_machine_selection_is_pinned_to_the_intended_machine_name(self):
        start = self.workflow.index("      - name: Resolve and start the InstaBox machine")
        end = self.workflow.index("      - name: Ensure Data Centre R2 media and metadata buckets exist", start)
        machine = self.workflow[start:end]
        self.assertIn("INSTABOX_MACHINE_NAME: ${{ vars.INSTABOX_MACHINE_NAME || 'testagramdb' }}", self.workflow)
        self.assertIn('EXPECTED_MACHINE_NAME="${INSTABOX_MACHINE_NAME:-testagramdb}"', machine)
        self.assertIn('select(.name == $name)', machine)
        self.assertIn('if [[ "$actual_machine_name" != "$EXPECTED_MACHINE_NAME" ]]', machine)
        self.assertIn("Refusing to deploy to machine", machine)
        self.assertNotIn("if (.machines | length) == 1", machine)

    def test_machine_creation_start_and_readiness_timeouts_fail_closed(self):
        start = self.workflow.index("      - name: Resolve and start the InstaBox machine")
        end = self.workflow.index("      - name: Ensure Data Centre R2 media and metadata buckets exist", start)
        machine = self.workflow[start:end]
        self.assertIn('if [[ "$creation_succeeded" != true ]]', machine)
        self.assertIn('if [[ "$start_succeeded" != true ]]', machine)
        self.assertIn('if [[ "$state" != "running" || "$ready" != "true" ]]', machine)
        self.assertIn("did not become ready within 300 seconds", machine)
        self.assertIn("exit 1", machine)

    def test_r2_managed_domain_update_uses_validated_json_file_body(self):
        start = self.workflow.index("r2_managed_url=")
        end = self.workflow.index("managed_check_status=", start)
        request = self.workflow[start:end]
        self.assertIn("jq -cn '{enabled:false}' | tr -d '\\\\n' > \"$managed_request\"", request)
        self.assertIn("--data-binary \"@$managed_request\"", request)
        self.assertIn("-H 'Content-Type: application/json'", request)
        self.assertIn("result.enabled == false", self.workflow)
        self.assertNotIn("--data-raw", request)

    def test_compose_and_caddy_consume_the_persisted_name(self):
        compose = (ROOT / "deploy" / "self-hosted" / "docker-compose.yml").read_text(encoding="utf-8")
        caddy = (ROOT / "deploy" / "self-hosted" / "Caddyfile").read_text(encoding="utf-8")
        env_example = (ROOT / "deploy" / "self-hosted" / ".env.example").read_text(encoding="utf-8")
        self.assertIn("EDGE_PROXY_SHARED_TOKEN", compose)
        self.assertEqual(env_example.count("EDGE_PROXY_SHARED_TOKEN="), 1)
        self.assertIn("REPLACE_WITH_A_SEPARATE_RANDOM_32_BYTE_EDGE_TOKEN", env_example)
        self.assertIn("{$EDGE_PROXY_SHARED_TOKEN}", caddy)


if __name__ == "__main__":
    unittest.main()
