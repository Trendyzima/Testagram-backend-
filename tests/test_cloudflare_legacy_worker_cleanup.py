#!/usr/bin/env python3
"""Static safety contract for Cloudflare legacy Worker retirement."""

from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = ROOT / ".github/workflows/remove-orphaned-legacy-cloudflare-workers.yml"


class CloudflareLegacyWorkerCleanupContractTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.source = WORKFLOW.read_text(encoding="utf-8")

    def test_deletion_requires_explicit_manual_confirmation(self):
        self.assertIn("workflow_dispatch:", self.source)
        self.assertIn("confirm_remove_orphaned_workers:", self.source)
        self.assertIn("default: false", self.source)
        self.assertIn("if: inputs.confirm_remove_orphaned_workers == true", self.source)

    def test_every_worker_ingress_path_is_checked(self):
        self.assertIn("/workers/routes", self.source)
        self.assertIn("/workers/domains?per_page=100&page=", self.source)
        self.assertIn("/workers/scripts/$worker/subdomain", self.source)
        self.assertIn("previews_enabled", self.source)
        self.assertIn("still has a zone route", self.source)
        self.assertIn("still has a Worker Custom Domain", self.source)
        self.assertIn("workers.dev or preview ingress enabled/unknown", self.source)

    def test_account_resolution_uses_matching_zone_record(self):
        self.assertIn("awk -F '\\\\t' '$2==\"testagram.site\" {print $3; exit}'", self.source)
        self.assertNotIn('select(.id == $zone)', self.source)

    def test_background_execution_and_cross_worker_dependencies_are_checked(self):
        self.assertIn("/schedules", self.source)
        self.assertIn("/settings", self.source)
        self.assertIn("/script-settings", self.source)
        self.assertIn("tail_consumers", self.source)
        self.assertIn(".type == \"service\"", self.source)
        self.assertIn(".type == \"workflow\"", self.source)

    def test_only_exact_legacy_sources_are_candidates(self):
        self.assertIn('"testagram-api|supabase"', self.source)
        self.assertIn('"testagram-heartbeat|supabase"', self.source)
        self.assertIn('"xclone-9b4dz1-9b4dz1-8936-20260809|xclone"', self.source)
        self.assertNotIn('"testagram-federation|', self.source)

    def test_source_marker_must_match_candidate(self):
        self.assertIn('grep -Eiq "$marker" "$tmp/source"', self.source)
        self.assertIn("source no longer matches the expected", self.source)

    def test_no_other_cloudflare_resource_is_deleted(self):
        self.assertIn('cf_delete "/accounts/$account_id/workers/scripts/$worker"', self.source)
        self.assertNotIn('cf_delete "/zones/', self.source)
        self.assertNotIn('cf_delete "/accounts/$account_id/pages/', self.source)
        self.assertNotIn('cf_delete "/accounts/$account_id/r2/', self.source)


if __name__ == "__main__":
    unittest.main()
