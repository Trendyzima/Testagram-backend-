from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = ROOT / ".github" / "workflows" / "instabox-deploy.yml"
DEPLOYER = ROOT / "scripts" / "start-instabox-deployment.sh"


class TestInstaBoxB2ApplicationKeyWiring(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.workflow = WORKFLOW.read_text(encoding="utf-8")
        cls.deployer = DEPLOYER.read_text(encoding="utf-8")

    def test_ordinary_media_uses_only_ordinary_b2_credentials(self):
        self.assertIn("B2_S3_ACCESS_KEY_ID: ${{ secrets.B2_S3_ACCESS_KEY_ID }}", self.workflow)
        self.assertIn("B2_S3_SECRET_ACCESS_KEY: ${{ secrets.B2_S3_SECRET_ACCESS_KEY }}", self.workflow)
        self.assertNotIn("B2_S3_IDENTITY_EVIDENCE_ACCESS_KEY_ID", self.workflow)
        self.assertNotIn("B2_S3_IDENTITY_EVIDENCE_SECRET_ACCESS_KEY", self.workflow)
        self.assertNotIn("B2_IDENTITY_ACCESS_KEY_ID", self.workflow)
        self.assertNotIn("B2_IDENTITY_SECRET_ACCESS_KEY", self.workflow)

    def test_deployer_does_not_provision_or_validate_identity_evidence_bucket(self):
        for forbidden in (
            "mc mb --ignore-existing \"b2_identity/",
            "mc alias set b2_identity",
            "B2_IDENTITY_ACCESS_KEY_ID",
            "B2_IDENTITY_SECRET_ACCESS_KEY",
            "DC_B2_S3_IDENTITY_EVIDENCE_ACCESS_KEY_ID",
            "DC_B2_S3_IDENTITY_EVIDENCE_SECRET_ACCESS_KEY",
            "identity-evidence bucket must be configured",
            "identity evidence requires its own bucket-scoped",
        ):
            with self.subTest(forbidden=forbidden):
                self.assertNotIn(forbidden, self.workflow + self.deployer)
        self.assertIn('write_env_value B2_S3_IDENTITY_EVIDENCE_PRIVATE_BUCKET ""', self.deployer)

    def test_legacy_identity_storage_tokens_are_cleared_for_ordinary_deployments(self):
        self.assertIn('write_env_value IDENTITY_EVIDENCE_DATA_CENTRE_INTERNAL_TOKEN ""', self.deployer)
        self.assertIn('write_dc_env_value IDENTITY_EVIDENCE_DATA_CENTRE_INTERNAL_TOKEN ""', self.deployer)


if __name__ == "__main__":
    unittest.main()
