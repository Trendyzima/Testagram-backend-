#!/usr/bin/env python3
"""Regression tests for the Cloudflare-to-Caddy edge shared credential path."""

from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[1]


class EdgeProxySharedSecretTests(unittest.TestCase):
    def test_production_workflow_requires_and_passes_the_exact_secret(self):
        workflow = (ROOT / ".github" / "workflows" / "instabox-deploy.yml").read_text(encoding="utf-8")
        secret_ref = "TESTAGRAM_EDGE_PROXY_SHARED_TOKEN: ${{ secrets.TESTAGRAM_EDGE_PROXY_SHARED_TOKEN }}"
        self.assertIn(secret_ref, workflow)
        self.assertIn("Validate edge proxy shared credential", workflow)
        self.assertIn('[[ ! "${TESTAGRAM_EDGE_PROXY_SHARED_TOKEN:-}" =~ ^[A-Fa-f0-9]{64}$ ]]', workflow)
        self.assertIn("python3 tests/test_instabox_edge_proxy_secret.py -v", workflow)

    def test_deployment_script_validates_persists_and_forwards_same_secret(self):
        script = (ROOT / "scripts" / "start-instabox-deployment.sh").read_text(encoding="utf-8")
        self.assertIn('[[ "$TESTAGRAM_EDGE_PROXY_SHARED_TOKEN" =~ ^[A-Fa-f0-9]{64}$ ]]', script)
        self.assertIn('write_env_value EDGE_PROXY_SHARED_TOKEN "$TESTAGRAM_EDGE_PROXY_SHARED_TOKEN"', script)
        self.assertIn('--arg edge_proxy_token "$TESTAGRAM_EDGE_PROXY_SHARED_TOKEN"', script)
        self.assertIn("EDGE_PROXY_SHARED_TOKEN:$edge_proxy_token", script)
        self.assertNotIn("ensure_random_secret EDGE_PROXY_SHARED_TOKEN", script)

    def test_compose_and_caddy_consume_the_persisted_name(self):
        compose = (ROOT / "deploy" / "self-hosted" / "docker-compose.yml").read_text(encoding="utf-8")
        caddy = (ROOT / "deploy" / "self-hosted" / "Caddyfile").read_text(encoding="utf-8")
        env_example = (ROOT / "deploy" / "self-hosted" / ".env.example").read_text(encoding="utf-8")
        self.assertIn("EDGE_PROXY_SHARED_TOKEN", compose)
        self.assertIn("EDGE_PROXY_SHARED_TOKEN=", env_example)
        self.assertIn("{$EDGE_PROXY_SHARED_TOKEN}", caddy)


if __name__ == "__main__":
    unittest.main()
