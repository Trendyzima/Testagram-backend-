#!/usr/bin/env python3
"""Static safety checks for API Worker deployment and route activation order."""

from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = (ROOT / ".github" / "workflows" / "cloudflare-api-gateway.yml").read_text(encoding="utf-8")
WRANGLER = (ROOT / "infra" / "cloudflare-api-gateway" / "wrangler.toml").read_text(encoding="utf-8")


class CloudflareApiGatewayDeploymentOrderTests(unittest.TestCase):
    def test_api_gateway_targets_backend_api_origin_not_apex_caddy(self):
        expected = "8080-agq4me2xgrgx5ge52ts437tqea.instabox.tech"
        apex = "8443-agq4me2xgrgx5ge52ts437tqea.instabox.tech"
        self.assertIn(expected, WRANGLER)
        self.assertNotIn(apex, WRANGLER)
        worker_source = (ROOT / "infra" / "cloudflare-api-gateway" / "src" / "index.mjs").read_text(encoding="utf-8")
        self.assertIn(expected, worker_source)
        self.assertNotIn(apex, worker_source)

    def test_worker_is_not_publicly_routed_by_wrangler_before_secret_install(self):
        self.assertIn("workers_dev = false", WRANGLER)
        self.assertNotIn("routes =", WRANGLER)
        deploy = WORKFLOW.index("wrangler@4 deploy --config infra/cloudflare-api-gateway/wrangler.toml")
        secret = WORKFLOW.index("wrangler@4 secret put EDGE_PROXY_SHARED_TOKEN")
        attach = WORKFLOW.index("API edge secret installed before route attachment")
        self.assertLess(deploy, secret)
        self.assertLess(secret, attach)

    def test_route_attachment_is_explicit_and_verified_after_secret(self):
        secret = WORKFLOW.index("wrangler@4 secret put EDGE_PROXY_SHARED_TOKEN")
        post = WORKFLOW.index("-X POST")
        verify = WORKFLOW.index("Canonical API route was not verified after attachment")
        self.assertLess(secret, post)
        self.assertLess(post, verify)
        self.assertIn('--arg pattern "$HOSTNAME/*"', WORKFLOW)
        self.assertIn('--arg script "testagram-api-edge"', WORKFLOW)
        self.assertIn("{pattern:$pattern,script:$script}", WORKFLOW)

    def test_preflight_fails_closed_on_any_overlapping_route_owned_by_another_worker(self):
        self.assertIn('startswith($host + "/")', WORKFLOW)
        self.assertIn("partial or overlapping cutover", WORKFLOW)
        self.assertIn("An overlapping API route changed owner after preflight", WORKFLOW)

    def test_zone_is_exactly_the_expected_active_account_zone(self):
        self.assertIn(".result[0].account.id == $account", WORKFLOW)
        self.assertIn("exactly one active testagram.site zone in the pinned Cloudflare account", WORKFLOW)
        self.assertIn("Cloudflare zone/account changed after preflight", WORKFLOW)


if __name__ == "__main__":
    unittest.main()
