#!/usr/bin/env python3
"""Regression tests for the self-hosted backup's effective MinIO origin guard."""
from pathlib import Path
import re
import subprocess
import unittest

ROOT = Path(__file__).resolve().parents[1]


class SelfHostedBackupGuardTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.source = (ROOT / "scripts/self-hosted-backup.sh").read_text()
        match = re.search(
            r"validate_local_minio_endpoint\(\) \{.*?\n\}",
            cls.source,
            re.S,
        )
        if match is None:
            raise AssertionError("local MinIO endpoint validator is missing")
        cls.validator = match.group(0)

    def check(self, endpoint):
        command = self.validator + '\nvalidate_local_minio_endpoint "$1"'
        return subprocess.run(
            ["bash", "-c", command, "test", endpoint],
            capture_output=True,
            text=True,
            check=False,
        )

    def test_allows_empty_or_exact_bundled_compose_origin(self):
        for endpoint in ("", "http://object-storage:9000"):
            with self.subTest(endpoint=endpoint):
                self.assertEqual(self.check(endpoint).returncode, 0)

    def test_rejects_external_or_overridden_minio_origins(self):
        for endpoint in (
            "https://media.testagram.site",
            "http://127.0.0.1:9000",
            "https://example-bucket.s3.amazonaws.com",
        ):
            with self.subTest(endpoint=endpoint):
                self.assertNotEqual(self.check(endpoint).returncode, 0)

    def test_backup_documentation_matches_existing_b2_first_workflow_contract(self):
        workflow = (
            ROOT / ".github/workflows/instabox-production-backup.yml"
        ).read_text()
        docs = (ROOT / "deploy/self-hosted/BACKUP-RESTORE.md").read_text()
        self.assertIn("Backblaze B2", docs)
        self.assertIn("B2_S3_ENDPOINT", workflow)
        self.assertIn("B2_S3_BUCKET", workflow)
        self.assertIn("B2_BACKUP_BUCKET: ${{ secrets.B2_S3_BUCKET }}", workflow)
        self.assertNotIn("vars.B2_S3_PRIVATE_BUCKET ||", workflow)
        self.assertIn("B2_S3_ACCESS_KEY_ID", workflow)
        self.assertIn("R2_BACKUP_ENDPOINT", workflow)
        self.assertIn("fall back to the existing dedicated Cloudflare R2 backup", docs)
        self.assertIn("TESTAGRAM_BACKUP_RESTIC_PASSWORD", docs)

    def test_backup_includes_active_b2_r2_media_and_keeps_legacy_replication_fail_closed(self):
        self.assertIn("mc mirror --preserve \"b2/$B2_S3_BUCKET\" /backup/b2-media", self.source)
        self.assertIn("mc mirror --preserve \"r2/$CLOUDFLARE_R2_BUCKET\" /backup/r2-media", self.source)
        self.assertIn("mc mirror --preserve \"r2/$CLOUDFLARE_R2_METADATA_BUCKET\" /backup/r2-metadata", self.source)
        self.assertIn("media-objects.tsv", self.source)
        self.assertIn("MEDIA_REPLICATION_ENABLED is active", self.source)
        self.assertIn("Data Centre media is configured but its runtime container is missing", self.source)
        self.assertIn("media outbox is not drained", self.source)
        self.assertIn("for endpoint_var in MEDIA_S3_ENDPOINT R2_S3_ENDPOINT", self.source)


if __name__ == "__main__":
    unittest.main()
