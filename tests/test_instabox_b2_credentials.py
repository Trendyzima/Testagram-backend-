from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = ROOT / ".github" / "workflows" / "instabox-deploy.yml"


class TestInstaBoxB2ApplicationKeyWiring(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.workflow = WORKFLOW.read_text(encoding="utf-8")

    def test_uses_the_supplied_backblaze_application_key_secret_names_first(self):
        self.assertIn(
            "B2_ACCESS_KEY_ID: ${{ secrets.B2_APPLICATION_KEY_ID || secrets.B2_ACCESS_KEY_ID }}",
            self.workflow,
        )
        self.assertIn(
            "B2_SECRET_ACCESS_KEY: ${{ secrets.B2_APPLICATION_KEY || secrets.B2_SECRET_ACCESS_KEY }}",
            self.workflow,
        )
        self.assertIn("B2_S3_ACCESS_KEY_ID: ${{ secrets.B2_S3_ACCESS_KEY_ID }}", self.workflow)
        self.assertIn("B2_S3_SECRET_ACCESS_KEY: ${{ secrets.B2_S3_SECRET_ACCESS_KEY }}", self.workflow)
        self.assertIn("B2_S3_IDENTITY_EVIDENCE_ACCESS_KEY_ID: ${{ secrets.B2_S3_IDENTITY_EVIDENCE_ACCESS_KEY_ID }}", self.workflow)
        self.assertIn("B2_S3_IDENTITY_EVIDENCE_SECRET_ACCESS_KEY: ${{ secrets.B2_S3_IDENTITY_EVIDENCE_SECRET_ACCESS_KEY }}", self.workflow)
        self.assertIn("B2_MEDIA_ACCESS_KEY_ID: ${{ secrets.B2_S3_ACCESS_KEY_ID }}", self.workflow)
        self.assertIn("B2_IDENTITY_ACCESS_KEY_ID: ${{ secrets.B2_S3_IDENTITY_EVIDENCE_ACCESS_KEY_ID }}", self.workflow)
        self.assertNotIn("B2_S3_ACCESS_KEY_ID: ${{ secrets.B2_APPLICATION_KEY_ID", self.workflow)

    def test_rejects_r2_style_or_malformed_keys_before_b2_bucket_operations(self):
        key_id_check = self.workflow.index("The Backblaze B2 application key ID must be 25 characters")
        secret_check = self.workflow.index("The Backblaze B2 provisioning key is too short")
        bucket_create = self.workflow.index('mc mb --ignore-existing "b2_provision/$B2_BUCKET"')
        self.assertLess(key_id_check, bucket_create)
        self.assertLess(secret_check, bucket_create)
        self.assertIn("${#B2_ACCESS_KEY_ID} -ne 25", self.workflow)
        self.assertIn("${#B2_SECRET_ACCESS_KEY} -lt 30", self.workflow)
        self.assertIn("B2_MEDIA_ACCESS_KEY_ID", self.workflow)
        self.assertIn("B2_IDENTITY_ACCESS_KEY_ID", self.workflow)
        self.assertIn('mc stat "b2_media/$B2_BUCKET"', self.workflow)
        self.assertIn('mc stat "b2_identity/$B2_MEDIA_BUCKET"', self.workflow)


if __name__ == "__main__":
    unittest.main()
