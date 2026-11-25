from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[1]
DEPLOY = ROOT / "scripts" / "start-instabox-deployment.sh"
WORKFLOW = ROOT / ".github" / "workflows" / "instabox-deploy.yml"


class TestInstaBoxWebImageReleaseWiring(unittest.TestCase):
    def test_workflow_uses_the_published_flutter_main_release(self):
        workflow = WORKFLOW.read_text(encoding="utf-8")
        self.assertIn("TESTAGRAM_WEB_IMAGE: ghcr.io/trendyzima/testagram-web@sha256:b79c3895343d1fc6518cdcba9ddff236b6375c90676663f6b9e4f391bde7671f", workflow)
        self.assertNotIn("TESTAGRAM_WEB_IMAGE: ghcr.io/trendyzima/testagram-web:main", workflow)

    def test_production_deployment_verifies_live_native_signup_page(self):
        workflow = WORKFLOW.read_text(encoding="utf-8")
        self.assertIn("accept-web-authentication:", workflow)
        self.assertIn("needs: deploy", workflow)
        self.assertIn('base="https://www.testagram.site"', workflow)
        self.assertIn("https://api.testagram.site/api/auth/signup", workflow)
        self.assertIn('"$api_response"', workflow)
        self.assertIn('"$api_status" != "403"', workflow)
        self.assertIn('"$api_error" != "IDENTITY_REVIEW_REQUIRED"', workflow)
        probe_start = workflow.index('api_status="$(curl')
        probe_end = workflow.index(')"', probe_start)
        signup_probe = workflow[probe_start:probe_end]
        self.assertIn("-X POST", signup_probe)
        self.assertNotIn("--data", signup_probe)
        self.assertNotIn(" -d ", signup_probe)
        self.assertNotIn("INVALID_PASSWORD", workflow)
        self.assertNotIn('"password":"short"', workflow)
        self.assertIn('"/?mode=signup"', workflow)
        self.assertIn("for marker in \'Sign in\' \'Sign up\' \'Create account\' \'Reset password\'; do", workflow)
        self.assertIn("flutter_bootstrap.js", workflow)
        self.assertIn("main.dart.js", workflow)

    def test_existing_vps_env_files_are_normalized_before_sourcing(self):
        deploy = DEPLOY.read_text(encoding="utf-8")
        self.assertIn("normalize_env_file()", deploy)
        self.assertIn("sed -i 's/\\r$//' \"$env_file\"", deploy)
        self.assertLess(deploy.index('normalize_env_file "$BASE_ENV"'), deploy.index('source "$BASE_ENV"'))
        self.assertLess(deploy.index('normalize_env_file "$DC_ENV"'), deploy.index('source "$DC_ENV"'))
        remote_normalize = deploy.rindex("sed -i 's/\\r$//' \"$BASE_ENV\" \"$DC_ENV\"")
        self.assertLess(remote_normalize, deploy.rindex('source "$BASE_ENV"'))
        self.assertLess(remote_normalize, deploy.rindex('source "$DC_ENV"'))

    def test_deployment_preserves_the_workflow_selected_digest(self):
        deploy = DEPLOY.read_text(encoding="utf-8")
        self.assertIn('TESTAGRAM_WEB_IMAGE="${DEPLOY_WEB_IMAGE:-}"', deploy)
        self.assertIn("Flutter web release must be supplied as an immutable GHCR digest", deploy)
        self.assertNotIn(r"ghcr\\.io", deploy)
        self.assertIn('export TESTAGRAM_WEB_IMAGE="__TESTAGRAM_WEB_IMAGE_PLACEHOLDER__"', deploy)
        self.assertIn('REMOTE_COMMAND="${REMOTE_COMMAND//__TESTAGRAM_WEB_IMAGE_PLACEHOLDER__/$TESTAGRAM_WEB_IMAGE}"', deploy)
        self.assertNotIn('export TESTAGRAM_WEB_IMAGE="$TESTAGRAM_WEB_IMAGE"', deploy)
        self.assertIn('DEPLOY_WEB_IMAGE="${TESTAGRAM_WEB_IMAGE:?TESTAGRAM_WEB_IMAGE was not forwarded into detached deployment}"', deploy)
        self.assertIn('if ! grep -Fq "export TESTAGRAM_WEB_IMAGE=', deploy)
        self.assertIn('TESTAGRAM_WEB_IMAGE" =~ ^ghcr', deploy)
        self.assertIn('@sha256:[a-f0-9]{64}', deploy)
        self.assertNotIn("sha256:aabdefce3c7d961e1cb3324b2c3cbfae69b02694212653f0555fd5e951da7a2b", deploy)

    def test_web_image_is_forwarded_to_remote_and_pinned_by_digest(self):
        deploy = DEPLOY.read_text(encoding="utf-8")
        self.assertIn('DEPLOY_WEB_IMAGE="${TESTAGRAM_WEB_IMAGE:?TESTAGRAM_WEB_IMAGE was not forwarded into detached deployment}"', deploy)
        self.assertIn('export TESTAGRAM_WEB_IMAGE="__TESTAGRAM_WEB_IMAGE_PLACEHOLDER__"', deploy)
        self.assertIn('write_env_value TESTAGRAM_WEB_IMAGE "$DEPLOY_WEB_IMAGE"', deploy)
        self.assertIn('--arg web_image "$TESTAGRAM_WEB_IMAGE"', deploy)
        self.assertIn("TESTAGRAM_WEB_IMAGE:$web_image", deploy)
        self.assertIn('write_env_value TESTAGRAM_WEB_IMAGE "$web_image_digest"', deploy)
        self.assertIn('sudo docker pull --platform linux/amd64 "$web_image_ref"', deploy)


if __name__ == "__main__":
    unittest.main()
