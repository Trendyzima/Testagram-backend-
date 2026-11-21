#!/usr/bin/env python3
"""Static safety checks for the manually dispatched Cloudflare Worker cleanup."""

from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = (ROOT / ".github" / "workflows" / "remove-orphaned-legacy-cloudflare-workers.yml").read_text(
    encoding="utf-8"
)


class CloudflareWorkerCleanupGuardsTest(unittest.TestCase):
    def test_cleanup_requires_explicit_manual_confirmation(self):
        self.assertIn("confirm_remove_orphaned_workers:", WORKFLOW)
        self.assertIn("default: false", WORKFLOW)
        self.assertIn("if: inputs.confirm_remove_orphaned_workers == true", WORKFLOW)

    def test_inventory_is_paginated_and_routes_are_initialized(self):
        self.assertIn('"/zones?per_page=50&page=$page"', WORKFLOW)
        self.assertIn('"/accounts/$account_id/workers/scripts?per_page=100&page=$page"', WORKFLOW)
        self.assertIn('"/accounts/$account_id/workers/domains?per_page=100&page=$page"', WORKFLOW)
        self.assertIn(': > "$tmp/routes.tsv"', WORKFLOW)

    def test_custom_domain_bindings_block_candidate_deletion(self):
        self.assertIn('if awk -F \'\\t\' -v w="$worker" \'$2==w {found=1} END {exit !found}\' "$tmp/domains.tsv"; then', WORKFLOW)
        self.assertIn("still owns a Worker custom domain; refusing deletion", WORKFLOW)

    def test_api_worker_requires_canonical_handoff_and_live_health(self):
        self.assertIn('api_domain_count="$(awk', WORKFLOW)
        self.assertIn('canonical_api_domain_count="$(awk', WORKFLOW)
        self.assertIn('https://api.testagram.site/healthz', WORKFLOW)
        self.assertIn("jq -e '.ok == true and .service == \"testagram-api\"'", WORKFLOW)

    def test_all_candidate_consumers_are_checked_before_delete(self):
        self.assertIn('/workers/routes', WORKFLOW)
        self.assertIn('/schedules', WORKFLOW)
        self.assertIn('tail_consumers', WORKFLOW)
        self.assertIn('type == "service" and .service == $target', WORKFLOW)
        self.assertIn('/content/v2', WORKFLOW)
        self.assertIn('cf_delete "/accounts/$account_id/workers/scripts/$worker"', WORKFLOW)

    def test_active_web_and_cdn_workers_are_not_candidates(self):
        self.assertIn('"testagram-api"', WORKFLOW)
        self.assertIn('"testagram-heartbeat"', WORKFLOW)
        self.assertIn('"testagram-federation"', WORKFLOW)
        self.assertIn('"xclone-9b4dz1-9b4dz1-8936-20260809"', WORKFLOW)
        self.assertNotIn('"testagram-web"\n', WORKFLOW[WORKFLOW.find("candidates=("):WORKFLOW.find(")")])
        self.assertNotIn('"testagram-cdn"\n', WORKFLOW[WORKFLOW.find("candidates=("):WORKFLOW.find(")")])

    def test_cleanup_never_mutates_dns_or_routes(self):
        self.assertNotIn('cf_delete "/zones/', WORKFLOW)
        self.assertNotIn('cf_delete "/accounts/$account_id/workers/domains', WORKFLOW)
        self.assertNotIn('cf_delete "/zones/$zone_id/workers/routes', WORKFLOW)


if __name__ == "__main__":
    unittest.main()
