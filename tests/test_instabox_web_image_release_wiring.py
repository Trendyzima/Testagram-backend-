from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[1]
DEPLOY = ROOT / "scripts" / "start-instabox-deployment.sh"
WORKFLOW = ROOT / ".github" / "workflows" / "instabox-deploy.yml"


class TestInstaBoxWebImageReleaseWiring(unittest.TestCase):
    def test_workflow_uses_the_published_flutter_main_release(self):
        workflow = WORKFLOW.read_text(encoding="utf-8")
        self.assertIn("TESTAGRAM_WEB_IMAGE: ghcr.io/trendyzima/testagram-web@sha256:58017cbd5a7266653dab0f7df1877d852dc13ee25aab71f26245eb5a60d85a61", workflow)
        self.assertNotIn("TESTAGRAM_WEB_IMAGE: ghcr.io/trendyzima/testagram-web:main", workflow)

    def test_deployment_preserves_the_workflow_selected_digest(self):
        deploy = DEPLOY.read_text(encoding="utf-8")
        self.assertIn('TESTAGRAM_WEB_IMAGE="${DEPLOY_WEB_IMAGE:-}"', deploy)
        self.assertIn("Flutter web release must be supplied as an immutable GHCR digest", deploy)
        self.assertNotIn("sha256:aabdefce3c7d961e1cb3324b2c3cbfae69b02694212653f0555fd5e951da7a2b", deploy)

    def test_web_image_is_forwarded_to_remote_and_pinned_by_digest(self):
        deploy = DEPLOY.read_text(encoding="utf-8")
        self.assertIn('DEPLOY_WEB_IMAGE="${TESTAGRAM_WEB_IMAGE:-}"', deploy)
        self.assertIn('export TESTAGRAM_WEB_IMAGE="__TESTAGRAM_WEB_IMAGE_PLACEHOLDER__"', deploy)
        self.assertIn('REMOTE_COMMAND="${REMOTE_COMMAND//__TESTAGRAM_WEB_IMAGE_PLACEHOLDER__/$TESTAGRAM_WEB_IMAGE}"', deploy)
        self.assertIn('TESTAGRAM_WEB_IMAGE" =~ ^ghcr\.io/trendyzima/testagram-web@sha256:[a-f0-9]{64}$', deploy)
        self.assertIn('write_env_value TESTAGRAM_WEB_IMAGE "$DEPLOY_WEB_IMAGE"', deploy)
        self.assertIn('--arg web_image "$TESTAGRAM_WEB_IMAGE"', deploy)
        self.assertIn("TESTAGRAM_WEB_IMAGE:$web_image", deploy)
        self.assertIn('write_env_value TESTAGRAM_WEB_IMAGE "$web_image_digest"', deploy)
        self.assertIn('sudo docker pull --platform linux/amd64 "$web_image_ref"', deploy)


if __name__ == "__main__":
    unittest.main()
