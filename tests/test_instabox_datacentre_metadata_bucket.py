from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[1]
DEPLOY = ROOT / "scripts" / "start-instabox-deployment.sh"
WORKFLOW = ROOT / ".github" / "workflows" / "instabox-deploy.yml"


class TestInstaBoxDataCentreMetadataBucketInput(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.deploy = DEPLOY.read_text(encoding="utf-8")
        cls.workflow = WORKFLOW.read_text(encoding="utf-8")

    def test_preserves_the_r2_metadata_bucket_from_github_input_across_env_sources(self):
        self.assertIn(
            'DEPLOY_R2_METADATA_BUCKET="${CLOUDFLARE_R2_METADATA_BUCKET:-}"',
            self.deploy,
        )
        self.assertIn(
            'write_dc_env_value CLOUDFLARE_R2_METADATA_BUCKET "$DEPLOY_R2_METADATA_BUCKET"',
            self.deploy,
        )
        self.assertIn(
            'CLOUDFLARE_R2_METADATA_BUCKET="$DEPLOY_R2_METADATA_BUCKET"',
            self.deploy,
        )
        self.assertNotIn("DC_R2_METADATA_BUCKET", self.deploy)

    def test_forwards_the_metadata_bucket_through_the_instabox_exec_payload(self):
        self.assertIn('r2_metadata_bucket="${DEPLOY_R2_METADATA_BUCKET:-}"', self.deploy)
        self.assertIn('--arg r2_metadata_bucket "$r2_metadata_bucket"', self.deploy)
        self.assertIn("CLOUDFLARE_R2_METADATA_BUCKET:$r2_metadata_bucket", self.deploy)

    def test_verifies_the_exact_metadata_bucket_in_the_live_media_container(self):
        self.assertIn(
            'verify_container_env "$DC_ENV" "$DATACENTRE/backend/docker-compose.yml" media CLOUDFLARE_R2_METADATA_BUCKET "$DEPLOY_R2_METADATA_BUCKET"',
            self.deploy,
        )
        self.assertIn("CLOUDFLARE_R2_METADATA_BUCKET:", self.workflow)
        self.assertIn("testagram-media-production-metadata", self.workflow)

    def test_media_profile_does_not_silently_skip_metadata_bucket_validation(self):
        self.assertIn("DATA_CENTRE_MEDIA_ENABLED=true requires complete B2/R2/MinIO metadata configuration", self.deploy)
        self.assertIn('if [[ "${DATA_CENTRE_MEDIA_ENABLED:-false}" == "true" && "${FRESH_MEDIA_ENABLED:-false}" == "true" ]]; then', self.deploy)


if __name__ == "__main__":
    unittest.main()
