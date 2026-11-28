#!/usr/bin/env python3
"""Regression guard: Cloudflare R2 is ordinary media only, never identity evidence."""
from pathlib import Path
import unittest

WORKFLOW = Path(__file__).resolve().parents[1] / ".github/workflows/instabox-deploy.yml"


class R2ManagedDomainRequestTests(unittest.TestCase):
    def test_r2_provisioning_contains_only_general_media_buckets(self):
        source = WORKFLOW.read_text(encoding="utf-8")
        self.assertIn('R2_MEDIA_BUCKET:', source)
        self.assertIn('R2_METADATA_BUCKET:', source)
        self.assertNotIn('R2_IDENTITY_BUCKET', source)
        self.assertNotIn('CLOUDFLARE_R2_IDENTITY_EVIDENCE_PRIVATE_BUCKET', source)
        self.assertNotIn('testagram-identity-evidence-production-r2', source)

    def test_identity_evidence_is_not_provisioned_by_ordinary_deployment(self):
        source = WORKFLOW.read_text(encoding="utf-8")
        self.assertNotIn("Provision dedicated private B2 identity-evidence bucket", source)
        self.assertNotIn("B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET", source)
        self.assertNotIn("B2_S3_IDENTITY_EVIDENCE_ACCESS_KEY_ID", source)
        self.assertNotIn("B2_S3_IDENTITY_EVIDENCE_SECRET_ACCESS_KEY", source)
        self.assertNotIn("R2 identity-evidence bucket", source)
        self.assertNotIn("R2_IDENTITY_BUCKET", source)


if __name__ == "__main__":
    unittest.main()
