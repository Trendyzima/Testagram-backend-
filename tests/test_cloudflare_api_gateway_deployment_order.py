#!/usr/bin/env python3
"""Static safety checks for API Worker deployment and route activation order."""

from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = (ROOT / ".github" / "workflows" / "cloudflare-api-gateway.yml").read_text(encoding="utf-8")
WRANGLER = (ROOT / "infra" / "cloudflare-api-gateway" / "wrangler.toml").read_text(encoding="utf-8")
CADDYFILE = (ROOT / "deploy" / "self-hosted" / "Caddyfile").read_text(encoding="utf-8")
DEPLOYMENT = (ROOT / "scripts" / "start-instabox-deployment.sh").read_text(encoding="utf-8")


class CloudflareApiGatewayDeploymentOrderTests(unittest.TestCase):
    def test_api_gateway_targets_the_dedicated_caddy_8443_ingress(self):
        expected = "8443-agq4me2xgrgx5ge52ts437tqea.instabox.tech"
        bypass = "8080-agq4me2xgrgx5ge52ts437tqea.instabox.tech"
        self.assertIn(expected, WRANGLER)
        self.assertNotIn(bypass, WRANGLER)
        worker_source = (ROOT / "infra" / "cloudflare-api-gateway" / "src" / "index.mjs").read_text(encoding="utf-8")
        self.assertIn(expected, worker_source)
        self.assertNotIn(bypass, worker_source)

    def test_worker_is_not_publicly_routed_by_wrangler_before_secret_install(self):
        self.assertIn("workers_dev = false", WRANGLER)
        self.assertNotIn("routes =", WRANGLER)
        deploy = WORKFLOW.index("wrangler@4 deploy --config infra/cloudflare-api-gateway/wrangler.toml")
        secret = WORKFLOW.index("wrangler@4 secret put EDGE_PROXY_SHARED_TOKEN")
        attach = WORKFLOW.index("API edge secret installed before route attachment")
        self.assertLess(deploy, secret)
        self.assertLess(secret, attach)

    def test_mail_event_queue_and_secrets_are_installed_before_subscription_activation(self):
        deploy = WORKFLOW.index("wrangler@4 deploy --config infra/cloudflare-api-gateway/wrangler.toml")
        edge_secret = WORKFLOW.index("wrangler@4 secret put EDGE_PROXY_SHARED_TOKEN")
        route_verified = WORKFLOW.index("Canonical API route was not verified after attachment")
        subscription = WORKFLOW.index("queues subscription create testagram-mail-delivery-events")
        self.assertLess(deploy, edge_secret)
        self.assertLess(edge_secret, route_verified)
        self.assertLess(route_verified, subscription)
        self.assertIn("ensure_queue \"testagram-mail-delivery-events\"", WORKFLOW)
        self.assertIn("ensure_queue \"testagram-mail-delivery-events-dlq\"", WORKFLOW)
        self.assertNotIn("TESTAGRAM_CLOUDFLARE_EVENTS_TOKEN", WORKFLOW)
        self.assertIn('queue = "testagram-mail-delivery-events"', WRANGLER)
        self.assertIn('dead_letter_queue = "testagram-mail-delivery-events-dlq"', WRANGLER)

    def test_route_attachment_is_explicit_and_verified_after_secret(self):
        secret = WORKFLOW.index("wrangler@4 secret put EDGE_PROXY_SHARED_TOKEN")
        post = WORKFLOW.index('--data-binary @"$RUNNER_TEMP/edge-route.json"')
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

    def test_mail_events_reuse_the_existing_edge_token_without_a_second_secret(self):
        deploy_script = (ROOT / "scripts" / "start-instabox-deployment.sh").read_text(encoding="utf-8")
        compose = (ROOT / "deploy" / "self-hosted" / "docker-compose.yml").read_text(encoding="utf-8")
        self.assertNotIn("TESTAGRAM_CLOUDFLARE_EVENTS_TOKEN", deploy_script)
        self.assertIn("EDGE_PROXY_SHARED_TOKEN: ${EDGE_PROXY_SHARED_TOKEN:?Set EDGE_PROXY_SHARED_TOKEN in .env}", compose)
        self.assertNotIn("TESTAGRAM_CLOUDFLARE_EVENTS_TOKEN", WORKFLOW)

    def test_caddy_api_ingress_uses_the_shared_token_not_proxy_rewritten_host_metadata(self):
        self.assertIn("@api_edge header X-Testagram-Edge-Token {$EDGE_PROXY_SHARED_TOKEN}", CADDYFILE)
        self.assertNotIn("header X-Forwarded-Host api.testagram.site", CADDYFILE)
        self.assertIn("reverse_proxy api:8080", CADDYFILE)
        self.assertIn("header_up -X-Testagram-Edge-Token", CADDYFILE)
        self.assertIn('verify_container_env "$BASE_ENV" "$BASE_COMPOSE" caddy EDGE_PROXY_SHARED_TOKEN "$EDGE_PROXY_SHARED_TOKEN"', DEPLOYMENT)


if __name__ == "__main__":
    unittest.main()
